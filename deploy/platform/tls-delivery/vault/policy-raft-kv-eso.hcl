# Vault policy for the raft-kv ESO delivery identity (Phase B #8).
#
# Attach only to the Kubernetes-auth role used by ServiceAccount raft-kv-eso
# in the raft-kv namespace. Replace PKI_MOUNT if your engine is not at "pki".
#
# Explicitly omitted (do not add):
#   - list / sudo on sys/* or other mounts
#   - read arbitrary KV paths
#   - pki/root/* or sign-intermediate (CA key material)
#   - broad path "pki/*" { capabilities = ["read", "list"] }

# Issue leaf certs for the scoped PKI role only.
path "PKI_MOUNT/issue/raft-kv" {
  capabilities = ["create", "update"]
}

# Read issuing CA / chain returned as issuing_ca in the issue response.
path "PKI_MOUNT/cert/ca" {
  capabilities = ["read"]
}

path "PKI_MOUNT/ca" {
  capabilities = ["read"]
}

# Optional: read this role's config during bootstrap validation (not required at runtime).
path "PKI_MOUNT/roles/raft-kv" {
  capabilities = ["read"]
}
