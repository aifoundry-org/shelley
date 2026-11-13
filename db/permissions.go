package db

import "shelley.exe.dev/internal/sqlitefs"

func protectDatabaseFiles(dsn string) error { return sqlitefs.Protect(dsn) }
