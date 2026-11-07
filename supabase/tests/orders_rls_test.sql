-- SC-003: anon and authenticated can't read or write the app tables directly,
-- checked layer by layer, plus the constraints the API relies on (FR-005).
-- Runs inside a transaction that is rolled back, so it never changes the seed.
begin;
create extension if not exists pgtap with schema extensions;
set search_path = extensions, public;

select plan(22);

-- Layer 3 (catalog): every table in `app` has RLS on, including tables added
-- after this test was written.
select is_empty(
  $$ select tablename from pg_tables where schemaname = 'app' and not rowsecurity $$,
  'every table in schema app has row level security enabled'
);

-- Layer 2: the API roles can't even enter the schema.
select ok(not has_schema_privilege('anon', 'app', 'usage'), 'anon has no usage on schema app');
select ok(not has_schema_privilege('authenticated', 'app', 'usage'), 'authenticated has no usage on schema app');

-- No table privilege for them either, however it was granted (directly or
-- through PUBLIC: has_table_privilege follows role membership).
select is_empty(
  $$ select r.rolname || ' on ' || c.relname
     from pg_class c
     cross join (values ('anon'), ('authenticated')) as r (rolname)
     where c.relnamespace = 'app'::regnamespace and c.relkind in ('r', 'v', 'm')
       and has_table_privilege(r.rolname, c.oid, 'SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER') $$,
  'anon and authenticated hold no privilege on any table in schema app'
);

-- Behaviour: try it as each role.
set local role anon;
select throws_ok($$ select * from app.orders $$, '42501', null, 'anon cannot read orders');
select throws_ok(
  $$ insert into app.orders (client_id, category, description)
     values ('11111111-1111-4111-8111-111111111111', 'plumbing', 'Inserted by anon') $$,
  '42501', null, 'anon cannot insert orders');
reset role;

-- authenticated with a real client's JWT claims, as PostgREST would set them
set local role authenticated;
select set_config('request.jwt.claims',
  '{"sub":"11111111-1111-4111-8111-111111111111","role":"authenticated"}', true);
select throws_ok($$ select * from app.orders $$, '42501', null,
  'a signed-in client cannot read orders directly');
select throws_ok(
  $$ insert into app.orders (client_id, category, description)
     values ('11111111-1111-4111-8111-111111111111', 'plumbing', 'Inserted around the API') $$,
  '42501', null, 'a signed-in client cannot insert orders directly');
select throws_ok($$ update app.orders set status = 'open' $$, '42501', null,
  'a signed-in client cannot update orders directly');
select throws_ok($$ delete from app.orders $$, '42501', null,
  'a signed-in client cannot delete orders directly');
select throws_ok($$ select * from app.accounts $$, '42501', null,
  'a signed-in client cannot read accounts directly');
reset role;

-- Layer 3 (behaviour): simulate the mistake of granting access to the Data
-- API role. RLS with no policy must still hide every row.
grant usage on schema app to authenticated;
grant select on app.orders to authenticated;
set local role authenticated;
select is((select count(*) from app.orders), 0::bigint,
  'after a mistaken grant, RLS still returns no orders to authenticated');
reset role;
revoke select on app.orders from authenticated;
revoke usage on schema app from authenticated;

-- FR-005 and the other constraints the API relies on (as postgres, the API's role).
create function pg_temp.new_order(text) returns void language sql as $$
  insert into app.orders (client_id, category, description)
  values ('11111111-1111-4111-8111-111111111111', 'plumbing', $1)
$$;
select throws_ok($$ select pg_temp.new_order(repeat('a', 9)) $$, '23514', null,
  'a description of 9 characters is rejected');
select lives_ok($$ select pg_temp.new_order(repeat('a', 10)) $$,
  'a description of exactly 10 characters is accepted');
select lives_ok($$ select pg_temp.new_order(repeat('a', 500)) $$,
  'a description of exactly 500 characters is accepted');
select throws_ok($$ select pg_temp.new_order(repeat('a', 501)) $$, '23514', null,
  'a description of 501 characters is rejected');
select throws_ok($$ select pg_temp.new_order('   too short   ') $$, '23514', null,
  'spaces do not count toward the minimum');
select throws_ok($$ select pg_temp.new_order(repeat(E'\n', 12)) $$, '23514', null,
  'newlines do not count toward the minimum');
select throws_ok($$ select pg_temp.new_order(repeat('a', 10) || repeat(' ', 600)) $$, '23514', null,
  'the 500 cap applies to what is stored, padding included');
select throws_ok(
  $$ insert into app.orders (client_id, category, description)
     values ('11111111-1111-4111-8111-111111111111', 'gardening', 'A category that does not exist') $$,
  '23503', null, 'an unknown category is rejected');
select throws_ok(
  $$ insert into app.orders (client_id, category, description, status)
     values ('11111111-1111-4111-8111-111111111111', 'plumbing', 'Status outside the list', 'paid') $$,
  '23514', null, 'an unknown status is rejected');
select is(
  (select status from app.orders where description = repeat('a', 10)),
  'open', 'a new order starts open');

select * from finish();
rollback;
