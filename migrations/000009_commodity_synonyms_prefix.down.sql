-- Drop the prefix expansion column. Guard the data cleanup so the migration is
-- safe to run even when the column was already removed (for example after a
-- partially-applied migration).
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM information_schema.columns
         WHERE table_schema = current_schema()
           AND table_name = 'commodity_synonyms'
           AND column_name = 'prefix_expansion'
    ) THEN
        UPDATE commodity_synonyms SET prefix_expansion = NULL;
    END IF;
END $$;

ALTER TABLE commodity_synonyms DROP COLUMN IF EXISTS prefix_expansion;
