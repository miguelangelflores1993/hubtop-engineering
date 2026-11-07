-- SC-003: anon and authenticated can't read or write the app tables directly.
-- Plus the constraints the API relies on. Runs inside a transaction that is
-- rolled back, so it never changes the seed.
begin;
create extension if not exists pgtap with schema extensions;
set search_path = extensions, public;

select plan(14);

-- 1. Every table in `app` has RLS on, including tables added after this test.
select is_empty(
  $$ select tablename from pg_tables where schemaname = 'app' and not rowsecurity $$,
  'every table in schema app has row level security enabled'
);

-- 2. The schema is not exposed through the Data API.
select ok(
  not has_schema_privilege('anon', 'app', 'usage'),
  'anon has no usage on schema app'
);
select ok(
  not has_schema_privilege('authenticated', 'app', 'usage'),
  'authenticated has no usage on schema app'
);

-- 3. No table privilege for the API roles, on any table, now or later.
select is_empty(
  $$ select table_name || ' ' || grantee || ' ' || privilege_type
     from information_schema.role_table_grants
     where table_schema = 'app' and grantee in ('anon', 'authenticated', 'public') $$,
  'no table in schema app grants anything to anon, authenticated or public'
);

-- 4. Behaviour, not just catalog: try it as each role.
set local role anon;
select throws_ok(
  $$ select * from app.orders $$, '42501', null,
  'anon cannot read orders'
);
reset role;

-- authenticated with a real client's JWT claims, as PostgREST would set them
set local role authenticated;
select set_config('request.jwt.claims',
  '{"sub":"11111111-1111-4111-8111-111111111111","role":"authenticated"}', true);
select throws_ok(
  $$ select * from app.orders $$, '42501', null,
  'a signed-in client cannot read orders directly'
);
select throws_ok(
  $$ insert into app.orders (client_id, category, description)
     values ('11111111-1111-4111-8111-111111111111', 'plumbing', 'Inserted around the API') $$,
  '42501', null,
  'a signed-in client cannot insert orders directly'
);
select throws_ok(
  $$ select * from app.accounts $$, '42501', null,
  'a signed-in client cannot read accounts directly'
);
reset role;

-- 5. Constraints the API relies on (as postgres, the API's role).
select throws_ok(
  $$ insert into app.orders (client_id, category, description)
     values ('11111111-1111-4111-8111-111111111111', 'plumbing', 'too short') $$,
  '23514', null,
  'a description under 10 characters is rejected'
);
select throws_ok(
  $$ insert into app.orders (client_id, category, description)
     values ('11111111-1111-4111-8111-111111111111', 'plumbing', '            padded   ') $$,
  '23514', null,
  'whitespace does not count toward the minimum length'
);
select throws_ok(
  $$ insert into app.orders (client_id, category, description)
     values ('11111111-1111-4111-8111-111111111111', 'gardening', 'A category that does not exist') $$,
  '23503', null,
  'an unknown category is rejected'
);
select throws_ok(
  $$ insert into app.orders (client_id, category, description, status)
     values ('11111111-1111-4111-8111-111111111111', 'plumbing', 'Status outside the list', 'paid') $$,
  '23514', null,
  'an unknown status is rejected'
);
select lives_ok(
  $$ insert into app.orders (client_id, category, description)
     values ('11111111-1111-4111-8111-111111111111', 'cleaning', 'Deep clean of a two-bedroom flat') $$,
  'a valid order is accepted'
);
select is(
  (select status from app.orders where description = 'Deep clean of a two-bedroom flat'),
  'open',
  'a new order starts open'
);

select * from finish();
rollback;
