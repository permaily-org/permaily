# The image of the hourly drift check (deploy/tofu/drift.tf, ADR 0035): AWS's
# base image for a custom Lambda runtime, OpenTofu's binary from its official
# minimal image, and two shell scripts. Both images are pinned by digest; the build
# downloads nothing else. Built and pushed by scripts/drift-image.sh.
#
# The configuration it plans is not in the image: each run fetches deploy/
# from the develop branch, so the image changes only with OpenTofu or the
# scripts.
FROM public.ecr.aws/lambda/provided:al2023.2026.10.31.17@sha256:2dc0f1f26731018b369932201d1dac295edd1730d1280f194486ae093ac39798
# The official image refuses to be a build stage; its -minimal variant holds
# only the binary, for this.
COPY --from=ghcr.io/opentofu/opentofu:1.13.9-minimal@sha256:84af0049634b7a7c455ab79c485818a8ef7f775cad3b2d42ce1c98ade720b5d1 /usr/local/bin/tofu /usr/local/bin/tofu
COPY bootstrap /var/runtime/bootstrap
COPY drift.sh /var/task/drift.sh
CMD ["drift"]
