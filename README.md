# Book Club

A small, private web app for a group of friends to run a book club: keep a shared want-to-read list, plan meetings, suggest books, and vote on what to read next. Ratings stay on Goodreads; every book links there.

Built with Go, [templ](https://templ.guide), [htmx](https://htmx.org), plain CSS and JavaScript, and PostgreSQL.

## Designed to stay flexible

The app records what your club does; it doesn't make you run the club a certain way.

- **Choose a voting method for each vote.** You can use one vote each, several votes each (or approval voting), points to spread, or ranked choice (counted by instant runoff or by Borda points). You can also skip ballots and just record a decision. Limits like "3 suggestions each" or "no voting for your own pick" are per-vote settings, stored as JSON. A new rule doesn't need a database change.
- **The count is a guide, not a cage.** After a vote closes, anyone can record what you actually decided: settle a tie, pick two books, or change your minds. Decisions are logged.
- **Meetings can have zero, one or many books.** Each one can carry a note, such as "chapters 1–10" or "read either".
- **Vote options can be books or free text.** That means you can also vote on themes, dates or places.
- **Duplicate a vote** to run a runoff or try different rules with the same options.
- **Everyone is equal.** Any member can add or edit books, meetings and votes. Every change goes into a history log that shows who changed what. Admins can only deactivate accounts and grant admin rights.

## Making votes trustworthy

- **Rules lock** once the first ballot is cast. Options that are on someone's ballot can't be removed.
- **Participation is public.** Everyone can see who has voted, so the number of ballots can be checked.
- **Every ballot is published when voting closes.**
  - In open votes, each ballot appears with the voter's name.
  - In secret votes, ballots appear without names. Each voter gets a receipt code so they can find their own ballot.
  - Ballots can be downloaded as CSV or JSON for an independent recount.
- **The count is shown step by step.** For instant runoff, you see each round, who was eliminated, and where the votes went.
- **A fingerprint is recorded when voting closes.** It's a SHA-256 hash of the options and ballots. The vote page recomputes it on every view and shows a warning if anything changed afterwards, even directly in the database.
- **The counting code is self-contained** (`internal/voting`) and has unit tests.

Secret ballots hide names from members in the app. Whoever runs the server can still see who cast which ballot in the database.

## Running locally

Requirements: Go 1.27+, PostgreSQL 14+.

```bash
cp .env.example .env        # then set DATABASE_URL
go install github.com/a-h/templ/cmd/templ@latest
templ generate
go run .
```

On first start, with no accounts in the database, the server log prints a one-time setup link:

```
WARN no accounts yet — open this link to create the first one url=http://localhost:8080/setup?token=…
```

Open it to create the first (admin) account and name the club. Then invite friends from the **Members** page. Any member can create single-use invite links, which last 14 days. There's no email. You send the link yourself.

Forgotten password: any member can create a reset link for another member from the Members page. The link lasts 48 hours, and creating one is recorded in the history.

When you edit a `.templ` file, run `templ generate` again (or `templ generate --watch` while developing). Database migrations in `internal/db/migrations` run automatically at startup.

Tests:

```bash
go test ./...
```

## Deploying

The app is a single binary configured with environment variables (see `.env.example`). Static files and migrations are embedded in the binary.

```bash
docker build -t bookclub .
docker run -p 8080:8080 -e DATABASE_URL=postgres://… -e BASE_URL=https://club.example.com bookclub
```

It runs anywhere that can run a container and reach PostgreSQL, such as Fly.io, Railway, a VPS or a home server. Put it behind HTTPS and set `BASE_URL` to the public `https://` address, so invite links are correct and cookies are marked Secure. `GET /healthz` reports whether the database is reachable.

## Layout

```
main.go                     startup, graceful shutdown
internal/config             environment / .env loading
internal/db                 connection + embedded SQL migrations
internal/store              data types and SQL queries
internal/voting             voting methods, tallying, fingerprints (+ tests)
internal/openlibrary        book search for auto-filling details
internal/web                routes, middleware (sessions, CSRF, CSP), handlers
internal/web/static         CSS, JS, vendored htmx
internal/views              templ templates
```
