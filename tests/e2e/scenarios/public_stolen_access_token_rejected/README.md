# public_stolen_access_token_rejected

Validates the public access-token/connect-proof rejection path used by Issue #116 security gates:

- valid connect proof is accepted;
- missing proof is rejected;
- expired proof is rejected;
- scope-mismatched proof is rejected;
- replayed proof is rejected.
