-- Spec 001: a client creates an order and a professional sees it.
--
-- Three layers of protection, from outside in:
--   1. The tables live in schema `app`, which the Data API doesn't expose
--      (config.toml exposes only `public` and `graphql_public`).
--   2. `anon` and `authenticated` have no USAGE on `app`, so nothing inside
--      it (tables, sequences, functions) is reachable by them, whatever
--      object-level defaults Postgres applies (functions get EXECUTE for
--      PUBLIC by default; without USAGE on the schema it can't be used).
--   3. RLS is enabled on every table with no policy for those roles, so even
--      a future grant by mistake returns zero rows instead of everyone's data.
-- The Go API is the only door: it connects as `postgres` (which bypasses RLS)
-- and decides authorization from the verified JWT.

create schema if not exists app;
revoke all on schema app from public, anon, authenticated;

-- Categories are reference data, loaded by the seed.
create table app.categories (
  slug text primary key check (slug ~ '^[a-z][a-z-]*$'),
  name text not null
);

-- One row per person. `id` is the same uuid as auth.users: the JWT `sub`.
-- A professional has one category in this slice; a client has none.
create table app.accounts (
  id uuid primary key references auth.users (id) on delete cascade,
  display_name text not null check (
    length(display_name) <= 80
    and length(regexp_replace(display_name, '^\s+|\s+$', '', 'g')) >= 1
  ),
  professional_category text references app.categories (slug),
  created_at timestamptz not null default now()
);

create table app.orders (
  id uuid primary key default gen_random_uuid(),
  -- restrict, not cascade: deleting a person must not silently erase orders
  -- that professionals already saw; closing them is an explicit flow
  client_id uuid not null references app.accounts (id) on delete restrict,
  category text not null references app.categories (slug),
  -- FR-005 is checked in the app for a fast message; the database enforces it anyway.
  -- The cap is on what is stored; the minimum ignores any whitespace (spaces,
  -- tabs, newlines), so padding can't fake a description.
  description text not null check (
    length(description) <= 500
    and length(regexp_replace(description, '^\s+|\s+$', '', 'g')) >= 10
  ),
  -- text + check instead of an enum: adding a status later is one migration, not a type change
  status text not null default 'open' check (status in ('open')),
  created_at timestamptz not null default now()
);

-- FR-003: the feed reads open orders of one category, newest first.
-- A partial index keeps it small: closed orders never enter it. `id` breaks
-- ties between orders created in the same instant, so the order is stable
-- and cursor pagination never skips or repeats a row.
create index orders_feed_idx on app.orders (category, created_at desc, id desc) where status = 'open';
-- "My orders" for the client.
create index orders_client_idx on app.orders (client_id, created_at desc, id desc);

alter table app.categories enable row level security;
alter table app.accounts enable row level security;
alter table app.orders enable row level security;
-- Tables added later must enable RLS too: supabase/tests/orders_rls_test.sql
-- fails if any table in `app` has it off.
