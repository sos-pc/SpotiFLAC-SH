# Catalog migrations

SQLite migrations applied by `backend/db/migrate.go` at server startup.

## Conventions

- Filename: `NNNN_short_description.sql` (zero-padded 4-digit prefix).
- Lexicographic order == application order. Never renumber an existing file.
- Forward-only: write a new migration to revert. No down migrations.
- Each statement ends with `;` followed by a newline. The runner splits on
  `";\n"` because `modernc.org/sqlite` only accepts one statement per `Exec`.
- A migration runs in a single transaction. On failure the entire file is
  rolled back, so partial writes are impossible.

## How to add one

1. Create `NNNN_my_change.sql` in this folder, where `NNNN` is the next
   sequential number.
2. Write only forward DDL/DML.
3. Build and run; the runner applies it on next startup and records the
   version in `schema_migrations`.

## Tracked entities (planned)

The catalog is the long-term memory of every track encountered, every
audio file on disk, every download attempt, and every playlist snapshot.
The current files in this folder define the schema incrementally; refer
to each migration's preamble for what it adds.
