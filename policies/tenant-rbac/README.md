# Membership-gated tenant RBAC v1

The internal TypeScript and Go compilers consume a structurally validated product
manifest and validate its binding/cross references before producing the same OpenFGA
1.1 model. Shared vectors live in contracts/fixtures/tenant-model-v1.json.

The object type is workspace. member is the direct membership relation. Sorted role
names map to r_N assignment relations and e_N effective-role relations. Sorted
permission names map to p_N relations. Each e_N and p_N intersects its grant with
member. A declared permission granted by no role is always false. Prefixes separate
roles, permissions and membership, including products that name a role member.

Ordinal mappings are scoped to the exact manifest/model pair. Adding or removing
names may change ordinals. Provisioning must pin the returned mapping, manifest
version and model ID together, and migrate tuples explicitly before activation;
never reuse old tuples under a new mapping. These functions do not migrate models
or expose a public SDK API yet.

modelHash is SHA-256 of UTF-8 compact model JSON with recursively sorted object
keys and preserved array order. Model content is ASCII and arrays are deterministic.
It identifies the model, not the entire product manifest or authorization to deploy.
Do not infer product/environment isolation from identical hashes; stores/configuration
and scoped SDK checks supply that boundary.

Live assertions: node tests/integration/tenant-model.mjs with the protected HTTPS
HEIMDALL_PROBE_OPENFGA_URL and HEIMDALL_PROBE_OPENFGA_KEY. Both language tests must
first match the complete expected model and hash in the fixture. The live probe
submits those exact models, checks every example role/membership combination,
checks another workspace and product principal, removes membership while retaining
role tuples, and requires denial with HIGHER_CONSISTENCY. Only owned stores are deleted.

Ordinary owner grants, delegation checks, provisioning validation, migration and
runtime errors remain separate implementation work; direct OpenFGA writer credentials
can write any assignment relation and are not an end-user policy boundary.
