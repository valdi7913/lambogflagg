-- Book club schema.
--
-- Design note: the club's *rules* (how many votes, how points work, whether
-- ballots are secret, ...) live in polls.settings (jsonb) and are interpreted
-- by application code. Nothing here hard-codes a particular way of running
-- the club, so new voting styles don't need schema changes.

CREATE TABLE users (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username       text        NOT NULL,
    display_name   text        NOT NULL,
    password_hash  text        NOT NULL,
    is_admin       boolean     NOT NULL DEFAULT false,
    invited_by     bigint      REFERENCES users(id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    deactivated_at timestamptz
);
CREATE UNIQUE INDEX users_username_lower ON users (lower(username));

CREATE TABLE sessions (
    token_hash  bytea       PRIMARY KEY,
    user_id     bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token  text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL
);
CREATE INDEX sessions_user ON sessions (user_id);

CREATE TABLE invites (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token       text        NOT NULL UNIQUE,
    note        text        NOT NULL DEFAULT '',
    created_by  bigint      REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    used_by     bigint      REFERENCES users(id) ON DELETE SET NULL,
    used_at     timestamptz,
    revoked_at  timestamptz
);

CREATE TABLE password_resets (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token       text        NOT NULL UNIQUE,
    user_id     bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_by  bigint      REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz
);

CREATE TABLE books (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title           text        NOT NULL,
    authors         text        NOT NULL DEFAULT '',
    year            integer,
    cover_url       text        NOT NULL DEFAULT '',
    goodreads_url   text        NOT NULL DEFAULT '',
    openlibrary_key text        NOT NULL DEFAULT '',
    notes           text        NOT NULL DEFAULT '',
    on_wishlist     boolean     NOT NULL DEFAULT true,
    added_by        bigint      REFERENCES users(id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX books_wishlist ON books (on_wishlist);

CREATE TABLE meetings (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title       text        NOT NULL DEFAULT '',
    starts_at   timestamptz,
    place       text        NOT NULL DEFAULT '',
    description text        NOT NULL DEFAULT '',
    created_by  bigint      REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX meetings_starts ON meetings (starts_at);

-- A meeting can cover zero, one or many books.
CREATE TABLE meeting_books (
    meeting_id  bigint      NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    book_id     bigint      NOT NULL REFERENCES books(id) ON DELETE RESTRICT,
    note        text        NOT NULL DEFAULT '',
    added_by    bigint      REFERENCES users(id) ON DELETE SET NULL,
    added_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (meeting_id, book_id)
);
CREATE INDEX meeting_books_book ON meeting_books (book_id);

CREATE TABLE polls (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title              text        NOT NULL,
    description        text        NOT NULL DEFAULT '',
    meeting_id         bigint      REFERENCES meetings(id) ON DELETE SET NULL,
    method             text        NOT NULL,
    settings           jsonb       NOT NULL DEFAULT '{}',
    status             text        NOT NULL DEFAULT 'nominating',
    closes_at          timestamptz,
    voting_opened_at   timestamptz,
    closed_at          timestamptz,
    ballot_fingerprint text        NOT NULL DEFAULT '',
    decision_note      text        NOT NULL DEFAULT '',
    decided_by         bigint      REFERENCES users(id) ON DELETE SET NULL,
    decided_at         timestamptz,
    created_by         bigint      REFERENCES users(id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX polls_status ON polls (status);
CREATE INDEX polls_meeting ON polls (meeting_id);

-- An option is either a book from the library or a free-text label
-- (so a vote can also be about a theme, a date, a place, ...).
CREATE TABLE poll_options (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    poll_id      bigint      NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
    book_id      bigint      REFERENCES books(id) ON DELETE RESTRICT,
    label        text        NOT NULL DEFAULT '',
    pitch        text        NOT NULL DEFAULT '',
    nominated_by bigint      REFERENCES users(id) ON DELETE SET NULL,
    chosen       boolean     NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (book_id IS NOT NULL OR label <> '')
);
CREATE UNIQUE INDEX poll_options_book ON poll_options (poll_id, book_id) WHERE book_id IS NOT NULL;
CREATE INDEX poll_options_book_id ON poll_options (book_id);

-- One ballot per member per poll. `data` shape depends on the poll method.
CREATE TABLE ballots (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    poll_id     bigint      NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
    user_id     bigint      NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    receipt     text        NOT NULL,
    data        jsonb       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (poll_id, user_id),
    UNIQUE (poll_id, receipt)
);

CREATE TABLE comments (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id     bigint      REFERENCES users(id) ON DELETE SET NULL,
    book_id     bigint      REFERENCES books(id) ON DELETE CASCADE,
    meeting_id  bigint      REFERENCES meetings(id) ON DELETE CASCADE,
    poll_id     bigint      REFERENCES polls(id) ON DELETE CASCADE,
    body        text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    edited_at   timestamptz,
    CHECK (num_nonnulls(book_id, meeting_id, poll_id) = 1)
);
CREATE INDEX comments_book ON comments (book_id) WHERE book_id IS NOT NULL;
CREATE INDEX comments_meeting ON comments (meeting_id) WHERE meeting_id IS NOT NULL;
CREATE INDEX comments_poll ON comments (poll_id) WHERE poll_id IS NOT NULL;

-- Append-only history of who changed what.
CREATE TABLE activity (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      bigint      REFERENCES users(id) ON DELETE SET NULL,
    action       text        NOT NULL,
    subject_type text        NOT NULL,
    subject_id   bigint      NOT NULL,
    summary      text        NOT NULL,
    details      jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX activity_subject ON activity (subject_type, subject_id, id DESC);
CREATE INDEX activity_recent ON activity (id DESC);

CREATE TABLE settings (
    key   text PRIMARY KEY,
    value text NOT NULL
);
INSERT INTO settings (key, value) VALUES ('club_name', 'Book Club');
