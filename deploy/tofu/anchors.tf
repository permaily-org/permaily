# Where the enclave keeps the commits of the records that must not be put
# back to an earlier copy (ADR 0016): one small object per change, locked in
# compliance mode by the enclave when it writes it, until a date nobody in the
# account can bring forward, the root user included. S3 refuses the lock on a
# bucket made without Object Lock, so the enclave cannot be given another kind
# of bucket; and a bucket that holds a locked object cannot be deleted.
#
# No default retention here: the period is the enclave's, compiled into the
# image (anchor_retention_days in deploy/enclave.json). Production keeps each
# commit ten years, so `tofu destroy` cannot remove this bucket once the
# enclave has written to it.

resource "aws_s3_bucket" "anchors" {
  bucket              = "permaily-anchors-${local.account_id}"
  object_lock_enabled = true
}

resource "aws_s3_bucket_versioning" "anchors" {
  bucket = aws_s3_bucket.anchors.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_public_access_block" "anchors" {
  bucket                  = aws_s3_bucket.anchors.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "anchors" {
  bucket = aws_s3_bucket.anchors.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# The staging deployment's commits. Its images lock each one for a day, and
# the lifecycle rule removes what is no longer locked.
resource "aws_s3_bucket" "anchors_staging" {
  bucket              = "permaily-anchors-staging-${local.account_id}"
  object_lock_enabled = true
}

resource "aws_s3_bucket_versioning" "anchors_staging" {
  bucket = aws_s3_bucket.anchors_staging.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_public_access_block" "anchors_staging" {
  bucket                  = aws_s3_bucket.anchors_staging.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "anchors_staging" {
  bucket = aws_s3_bucket.anchors_staging.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "anchors_staging" {
  bucket     = aws_s3_bucket.anchors_staging.id
  depends_on = [aws_s3_bucket_versioning.anchors_staging]

  rule {
    id     = "expire-test-commits"
    status = "Enabled"
    filter {}
    expiration {
      days = 8
    }
    noncurrent_version_expiration {
      noncurrent_days = 8
    }
  }
  rule {
    id     = "remove-delete-markers"
    status = "Enabled"
    filter {}
    expiration {
      expired_object_delete_marker = true
    }
  }
}

# The enclave reads every version of every object (a delete marker hides one
# from a plain listing without removing it), and writes each commit with its
# lock. It never deletes.
resource "aws_iam_role_policy" "anchors" {
  name = "record-commits"
  role = aws_iam_role.enclave_host.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = "s3:ListBucketVersions"
        Resource = [aws_s3_bucket.anchors.arn, aws_s3_bucket.anchors_staging.arn]
      },
      {
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:GetObjectVersion", "s3:GetObjectRetention", "s3:PutObject", "s3:PutObjectRetention"]
        Resource = ["${aws_s3_bucket.anchors.arn}/commits/*", "${aws_s3_bucket.anchors_staging.arn}/commits/*"]
      },
    ]
  })
}
