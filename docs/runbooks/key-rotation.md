# Master key rotation

Severity: planned. Rotate on a regular cadence (annually at minimum, immediately on any access-key compromise).

## Background

mqConnector encrypts stored broker passwords at rest with AES-256-GCM under a master key supplied via `MQC_MASTER_KEY` or `MQC_MASTER_KEYS` (multi-version). Phase 17c added the in-place rotation API.

Stored ciphertext is tagged with the key version (`enc:v{N}:base64`), so:
- The current key encrypts every new row.
- Older versions stay installed for decrypt only.
- After rotation, `RewrapPasswords` re-encrypts every row under the new version.

## Procedure

1. **Generate a fresh key**:
   ```sh
   openssl rand -hex 32
   ```

2. **Stage it as an additional version**. Append to `MQC_MASTER_KEYS`:
   ```sh
   # before:   v1=<old hex>
   # after:    v1=<old hex>,v2=<new hex>
   ```
   Restart the binary so it picks up both versions. The current is now v2 (highest), but every existing row decrypts under v1.

3. **Trigger in-place re-encryption**:
   ```sh
   curl -sk -X POST -H "Authorization: Bearer $TOKEN" \
     https://localhost:8443/api/v1/secrets/rotate
   ```
   Response includes `rewrapped_rows: N`. Confirm `skipped_rows == 0`.

   This endpoint also generates ANOTHER key + adds it as v3 — only use it when the process you want is "rotate, immediately rewrap". For the simpler "I already have v2, please rewrap":
   ```sh
   # Walk every connection through the API to trigger Update which
   # re-seals under the current key. There isn't yet a dedicated
   # "rewrap-only" endpoint; the rotate endpoint is the supported path.
   ```

4. **Confirm**:
   ```sh
   curl -sk -H "Authorization: Bearer $TOKEN" \
     https://localhost:8443/api/v1/secrets/status
   # { "enabled": true, "current": 2, "versions": [1, 2] }
   ```

5. **Drop the old version** after a cool-off period (a week is conservative). Remove `v1=…` from `MQC_MASTER_KEYS` and restart. The chain holds because every stored row is now `enc:v2:`.

## Failure modes

- **Restart with v2 unset**: any row written under v2 becomes unreadable. The binary refuses to start (Decrypt errors loudly on first read). Recovery: restore the v2 key, restart.
- **Network blip mid-rewrap**: the rotation endpoint walks rows sequentially; a failure leaves the table half-rewrapped (some `enc:v1:`, some `enc:v2:`). Both still decrypt under their respective key versions, so functional safety is preserved. Re-run rotate to converge.
- **Rotation accidentally rotates again**: the endpoint *generates a new key* in addition to rewrapping. If you just want to rewrap an existing pre-staged key, restart with the env var set; Encrypt + Decrypt then route the right way and a slower "edit every connection through the UI" works.

## Compliance evidence

Each rotation is audit-logged (`POST /api/v1/secrets/rotate`). The audit chain remains intact across rotation — verify with `/api/v1/audit/verify` after.
