variable "aws_profile" {
  description = "AWS CLI profile. There is no default profile on purpose."
  type        = string
  default     = "permaily"
}

variable "aws_region" {
  type    = string
  default = "us-east-2"
}

variable "domain" {
  description = "Cloudflare zone that holds the enclave subdomains."
  type        = string
  default     = "permaily.com"
}

variable "enclave_subdomains" {
  description = "Subdomains served by the enclave. Must match the hosts compiled into the image."
  type        = list(string)
  default     = ["app", "api", "mcp", "inbox", "mta-sts.inbox"]
}

variable "acme_account_uris" {
  description = "ACME accounts allowed to issue certificates for the enclave hosts: the URL the enclave reports in /status (tls.acme_account). The key of that account exists only sealed for the enclave. Empty allows any Let's Encrypt account, which is only for the first deployment, before the account exists."
  type        = list(string)
  default     = []
}

variable "instance_type" {
  description = "Graviton instance with Nitro Enclaves support. c7g.large = 2 vCPU / 4 GiB."
  type        = string
  default     = "c7g.large"
}

variable "enclave_cpus" {
  description = "vCPUs reserved for the enclave (on Graviton, 1 is allowed)."
  type        = number
  default     = 1
}

variable "enclave_memory_mib" {
  description = "Memory reserved for the enclave. Keep GOMEMLIMIT in nix/eif.nix below this."
  type        = number
  default     = 2048
}
