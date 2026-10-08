-- Rotating refresh-token families. Each login issues one row; a
-- refresh atomically revokes the consumed row and inserts its successor in the
-- same family. A presented token whose hash is absent (already rotated away)
-- is a reuse attempt and revokes the whole family. token_hash stores
-- sha256(token) so a leaked DB never exposes usable refresh tokens.
CREATE TABLE refresh_tokens (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    family_id TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    revoked BOOLEAN NOT NULL DEFAULT false,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX refresh_tokens_user_idx ON refresh_tokens (user_id);
CREATE INDEX refresh_tokens_family_idx ON refresh_tokens (family_id);

