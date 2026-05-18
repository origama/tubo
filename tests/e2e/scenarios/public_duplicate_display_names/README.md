# public_duplicate_display_names

Validates the Issue #116 service_id-first Discovery V2 model:

- duplicate `display_name` values are accepted as separate records when `service_id` differs;
- wrong service public key is rejected;
- publish lease for a different service ID is rejected;
- untrusted/expired publish leases are rejected.
