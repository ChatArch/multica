-- Intentionally a no-op (MUL-5400): the shipped up migration does not mutate
-- data. Development databases that applied the original backfill keep their
-- historical labels; reversing them here would reintroduce the same full-table
-- scans this change removes from the deployment path.
SELECT 1;
