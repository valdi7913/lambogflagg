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

## Languages

The app is in English and Icelandic.

- **Picking a language:** each person chooses with the *Íslenska / English* switch in the top bar, or on their account page. The choice is remembered on that device. Before someone chooses, the browser's language decides.
- **History:** entries are stored as message keys, so they show in each reader's language.
- **Adding phrases:**
  - English text is the key, wrapped in `t(ctx, "…")` in templates and `tr(r, "…")` or `s.flash(…)` in handlers.
  - Translations live in `internal/i18n/is.go`. `go test ./internal/i18n` fails if any phrase is missing or a translation's `%s`/`%d` placeholders don't match.

## Themes

There are four themes:

- **Automatic** (the default) follows the device's light or dark setting.
- **Light** and **Dark** fix the look regardless of the device.
- **Parchment** is book-themed: aged paper, ink-brown text, an oxblood accent, a top bar like a leather book spine, and leather-bound placeholder covers. Large titles use MedievalSharp with an illuminated first letter.

Each person picks a theme from the menu in the top bar or on their account page. The choice is remembered on that device. The server renders it straight into the page, so it doesn't flash the wrong colours while loading. Switching takes effect instantly.

MedievalSharp is served from `internal/web/static/fonts` rather than from Google, so no third-party requests are made. It's licensed under the SIL Open Font License, and the licence text is included there as `OFL.txt`.

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

It runs anywhere that can run a container and reach PostgreSQL, such as Fly.io, Railway, a VPS or a home server. Put it behind HTTPS and set `BASE_URL` to the public `https://` address, so invite links are correct and cookies are marked Secure. `GET /healthz` reports whether the database is reachable; `GET /livez` only reports that the app is running.

If the app sits behind a reverse proxy, set `CLIENT_IP_HEADER` to the header the proxy uses for the visitor's address (Fly.io: `Fly-Client-IP`). Without it, the login rate limit sees every visitor as the proxy and 10 failed logins by anyone would lock everyone out. Only set it when the app can't be reached except through that proxy.

## Hosting on Fly.io + Neon

The app runs on [Fly.io](https://fly.io) and the database on [Neon](https://neon.com)'s free plan. Both are set up in the repo: `fly.toml` configures Fly, and `.github/workflows/ci.yml` checks every pull request and deploys every merge to `main`.

Expected cost is a couple of dollars a month for Fly (a small machine that stops when nobody is using the app), nothing for Neon, plus the domain (about $10–11 a year for a `.com`). Fly asks for a payment card when you sign up. Check both pricing pages before you commit, since free plans change.

### How it behaves

- **Idle:** the machine stops when nobody has used the app for a few minutes, and Neon puts the database to sleep. The first visit afterwards takes a few seconds while both wake up.
- **Deadlines:** a vote whose deadline passes while the app is asleep is closed the moment someone next opens the app, the vote list or the home page. Ballots after the deadline are refused either way, and the fingerprint is recorded when the vote is closed.
- **One machine only.** Login rate limiting lives in memory, so the deploy runs with `--ha=false`. Don't scale to several machines.

### One-time setup

1. **Database.** In Neon, create a project in the region nearest your Fly region (London `aws-eu-west-2` goes with `lhr`, Frankfurt `aws-eu-central-1` with `fra`). Copy the connection string with **Connection pooling switched off**. The app's migrations use a session-level lock that pooled connections don't support.
2. **Fly app.** Install [flyctl](https://fly.io/docs/flyctl/install/) and run:
   ```bash
   fly auth login
   fly apps create YOUR-APP-NAME
   fly secrets set DATABASE_URL='postgresql://…' -a YOUR-APP-NAME
   ```
   Then edit `fly.toml`: replace both `YOUR-APP-NAME` placeholders, and set `TIMEZONE` and `primary_region` to suit.
3. **Deploy token.** Create one that can only deploy this app and add it to GitHub as a repository secret named `FLY_API_TOKEN` (Settings → Secrets and variables → Actions):
   ```bash
   fly tokens create deploy -a YOUR-APP-NAME
   ```
4. **Protect `main`.** In GitHub (Settings → Branches), require a pull request and require the `test` and `docker-build` checks to pass. Then only merged, passing code gets deployed.
5. **First deploy.** Commit and push `fly.toml`, or run `fly deploy --remote-only --ha=false` yourself once. From then on, merging to `main` deploys.
6. **First account.** Run `fly logs -a YOUR-APP-NAME` and open the setup link it prints (see "Running locally"). The link is new each time the app starts, so use the latest one.

Until the `FLY_API_TOKEN` secret exists, the deploy step is skipped and says so, so pushing the repo to GitHub first is safe.

### Your own domain

Buy a domain from a registrar, then:

```bash
fly certs add club.example.com -a YOUR-APP-NAME
fly certs show club.example.com -a YOUR-APP-NAME   # shows the DNS records to create
```

Create those records at your registrar, wait for `fly certs show` to report the certificate as issued, change `BASE_URL` in `fly.toml` to `https://club.example.com`, and merge that change. Invite and reset links use `BASE_URL`.

### Backups

Neon's free plan only keeps a few hours of history, so take your own copies, for instance monthly and before an important vote. You need `pg_dump` (part of the PostgreSQL client tools):

```bash
pg_dump 'postgresql://…' --format=custom --file=bookclub-$(date +%F).dump
```

Keep dumps somewhere private. They contain password hashes and everything members have written, so don't publish them, and don't upload them as GitHub Actions artifacts if the repository is public.

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
