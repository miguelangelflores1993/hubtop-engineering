# Feature Specification: A client creates an order and a professional sees it

**Feature Branch**: `001-client-creates-order`
**Created**: 2026-11-07
**Status**: Draft
**Input**: The first end-to-end flow of HubTop Lite. A client describes a job,
the API stores it, and a professional of the same category sees it. It is the
smallest slice that exercises every layer: two Flutter microapps, the Go API,
Postgres with RLS, and the identity taken from the JWT.

## Context

- One account, two modes. A person signs in once and acts as a client or as a
  professional; in this slice the two modes are two microapps inside one host
  app.
- Authentication is local Supabase Auth with seeded fake accounts. Sign-up,
  password recovery and social login are out of scope.
- Categories are seed data (`plumbing`, `electrical`, `cleaning`). A
  professional has exactly one category in this slice.
- Matching by distance, offers, payments and ratings come in later specs.

## User Scenarios & Testing

### User Story 1 - The client publishes an order (Priority: P1)

A signed-in client picks a category, writes what they need and publishes it.

**Why this priority**: without an order there is nothing for the rest of the
marketplace to act on.

**Acceptance Scenarios**:

1. **Given** a signed-in client, **When** they choose `plumbing`, write
   "The kitchen sink is leaking" and publish, **Then** they see the order in
   "My orders" with status `open`.
2. **Given** a signed-in client, **When** they publish with a description
   shorter than 10 characters, **Then** the app shows the validation message
   and nothing is sent to the API.
3. **Given** a request whose body names another account as the owner,
   **When** the API stores the order, **Then** the owner is the account in
   the JWT, never the one in the body.

### User Story 2 - The professional sees open orders of their category (Priority: P1)

A signed-in professional opens their feed and sees the open orders of their
category, newest first.

**Why this priority**: it closes the loop; it is the proof that the order went
through the API and the database and came back to a different person.

**Acceptance Scenarios**:

1. **Given** an open `plumbing` order, **When** a `plumbing` professional opens
   the feed, **Then** they see it with its description and how long ago it was
   published.
2. **Given** the same order, **When** an `electrical` professional opens the
   feed, **Then** they don't see it.
3. **Given** the same order, **When** a client who is not its owner queries
   orders, **Then** the order is not returned.

### Edge Cases

- A request without a JWT, or with an expired or forged one, gets `401` and
  touches no data.
- A professional with no category sees an empty feed with an explanation, not
  an error.
- An order is never visible through the database's public API: the only path
  to it is the Go API (RLS denies the `anon` and `authenticated` roles direct
  access).

## Requirements

- **FR-001**: The API MUST expose `POST /orders`, `GET /orders/feed` and
  `GET /orders/mine` (the client's "My orders" in story 1), all
  described in `openapi.yaml`.
- **FR-002**: The API MUST take the account id from the verified JWT (`sub`)
  and MUST ignore any owner field in the request.
- **FR-003**: `GET /orders/feed` MUST return only `open` orders of the
  caller's category, newest first, at most 50.
- **FR-004**: The schema MUST enable RLS on every table, with policies that
  deny direct reads and writes to `anon` and `authenticated`.
- **FR-005**: The client app MUST validate the description (10–500
  characters) before calling the API.
- **FR-006**: All seed data MUST be invented: names, emails on `example.test`,
  no real addresses or phone numbers.

## Success Criteria

- **SC-001**: The BDD scenarios above run as `.feature` files in the two
  microapps and pass.
- **SC-002**: Go tests cover FR-002 (a body with a foreign owner is ignored)
  and the `401` edge cases.
- **SC-003**: SQL tests prove that `anon` and `authenticated` can't read or
  write `orders` directly.
- **SC-004**: From a fresh clone, one documented command starts the stack with
  seed data, and the whole flow can be clicked through in under 30 minutes.
