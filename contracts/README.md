# Shared SDK contracts

Language-neutral contracts and fixtures define matching TypeScript/Go behavior.
Version 1 is under construction as Phase 1, not yet a complete released contract.

## Canonical human identity encoding v1

Input is an issuer/subject pair of nonempty Unicode scalar strings. Issuer is at
most 2048 UTF-8 bytes; subject at most 1024. Reject ill-formed Unicode. Preserve
strings exactly: no case folding, Unicode/URL normalization or trimming.
Issuer trust and token verification are separate runtime responsibilities;
encoding a pair does not create a verified principal.

Encode the compact JSON array of issuer and subject in ASCII. Escape quote and
backslash, use JSON short escapes for backspace/formfeed/newline/return/tab,
and lowercase Unicode escapes for remaining controls and every UTF-16 code unit
at or above U+007F. Supplementary scalars use their surrogate pair. Do not escape
slash, less-than, greater-than or ampersand. Prefix unpadded base64url with user:.

Fixtures contain inputs and exact expected IDs or invalid_argument. Both runners
also reject native malformed string representations: unpaired TS surrogates and
invalid Go UTF-8. Neither runtime silently repairs malformed input.

Changing encoding, scope interpretation or error meanings requires a new contract
version and migration review. Additive changes preserve existing vectors.
