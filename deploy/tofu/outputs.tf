output "instance_id" {
  value = aws_instance.enclave_host.id
}

output "public_ip" {
  value = aws_eip.enclave_host.public_ip
}

output "artifacts_bucket" {
  value = aws_s3_bucket.artifacts.bucket
}

output "region" {
  value = var.aws_region
}

output "hosts" {
  value = [for s in var.enclave_subdomains : "${s}.${var.domain}"]
}
