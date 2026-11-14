WITH advance AS (
  UPDATE public.test_retirement_sweeps
  SET position = $3::jsonb, complete = $3::jsonb = 'null'::jsonb
  WHERE id = $1 AND position = $2::jsonb AND NOT complete
  RETURNING id
)
INSERT INTO public.test_retirement_batches (id, position, batch)
SELECT id, $2::jsonb, $4::jsonb FROM advance
