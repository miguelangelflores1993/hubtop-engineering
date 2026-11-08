-- Fake data for local development and CI. Everything here is invented:
-- names, emails on example.test, and one shared demo password that only
-- exists in this local database.
--
--   ana@example.test     client
--   bruno@example.test   client
--   carla@example.test   professional, plumbing
--   diego@example.test   professional, electrical
--
-- Password for all of them: demo-password
-- Fixed uuids so tests and docs can refer to them.

insert into app.categories (slug, name) values
  ('plumbing', 'Plumbing'),
  ('electrical', 'Electrical'),
  ('cleaning', 'Cleaning');

with people (id, email) as (
  values
    ('11111111-1111-4111-8111-111111111111'::uuid, 'ana@example.test'),
    ('22222222-2222-4222-8222-222222222222'::uuid, 'bruno@example.test'),
    ('33333333-3333-4333-8333-333333333333'::uuid, 'carla@example.test'),
    ('44444444-4444-4444-8444-444444444444'::uuid, 'diego@example.test')
)
insert into auth.users (
  instance_id, id, aud, role, email, encrypted_password, email_confirmed_at,
  raw_app_meta_data, raw_user_meta_data, created_at, updated_at,
  -- GoTrue reads these as strings: null breaks sign-in
  confirmation_token, recovery_token, email_change_token_new, email_change
)
select
  '00000000-0000-0000-0000-000000000000', id, 'authenticated', 'authenticated', email,
  extensions.crypt('demo-password', extensions.gen_salt('bf')), now(),
  '{"provider":"email","providers":["email"]}', '{}', now(), now(),
  '', '', '', ''
from people;

-- Email sign-in also needs an identity per user.
insert into auth.identities (id, user_id, provider_id, provider, identity_data, last_sign_in_at, created_at, updated_at)
select gen_random_uuid(), id, id::text, 'email',
       jsonb_build_object('sub', id::text, 'email', email, 'email_verified', true),
       now(), now(), now()
from auth.users
where email like '%@example.test';

insert into app.accounts (id, display_name, professional_category) values
  ('11111111-1111-4111-8111-111111111111', 'Ana Souza', null),
  ('22222222-2222-4222-8222-222222222222', 'Bruno Lima', null),
  ('33333333-3333-4333-8333-333333333333', 'Carla Rocha', 'plumbing'),
  ('44444444-4444-4444-8444-444444444444', 'Diego Alves', 'electrical');

insert into app.orders (client_id, category, description, created_at) values
  ('11111111-1111-4111-8111-111111111111', 'plumbing', 'The kitchen sink is leaking under the cabinet.', now() - interval '2 hours'),
  ('22222222-2222-4222-8222-222222222222', 'plumbing', 'Shower has almost no water pressure.', now() - interval '30 minutes'),
  ('22222222-2222-4222-8222-222222222222', 'electrical', 'Two sockets in the living room stopped working.', now() - interval '1 day');
