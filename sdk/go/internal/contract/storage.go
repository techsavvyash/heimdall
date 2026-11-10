package contract

import "regexp"

type MigrationChecksum struct {
	Version  int
	Checksum string
}
type StoragePolicy struct {
	ReaderVersion, WriterVersion, MinDatabaseVersion, MaxDatabaseVersion int
	Migrations                                                           []MigrationChecksum
}
type ProtocolRange struct{ Min, Max int }
type StorageMetadata struct {
	DatabaseVersion  int
	State            string
	Readers, Writers ProtocolRange
	Migrations       []MigrationChecksum
}

var migrationChecksumPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ValidateStorageCompatibility is a read-only check following metadata schema validation.
func ValidateStorageCompatibility(m StorageMetadata, p StoragePolicy) error {
	for _, v := range []int{p.ReaderVersion, p.WriterVersion, p.MinDatabaseVersion, p.MaxDatabaseVersion} {
		if v < 1 || v > 1000 {
			return ErrInvalidArgument
		}
	}
	if p.MinDatabaseVersion > p.MaxDatabaseVersion || len(p.Migrations) != p.MaxDatabaseVersion {
		return ErrInvalidArgument
	}
	for i, v := range p.Migrations {
		if v.Version != i+1 || !migrationChecksumPattern.MatchString(v.Checksum) {
			return ErrInvalidArgument
		}
	}
	if m.State != "ready" || m.DatabaseVersion < p.MinDatabaseVersion || m.DatabaseVersion > p.MaxDatabaseVersion || m.Readers.Min > m.Readers.Max || m.Writers.Min > m.Writers.Max || p.ReaderVersion < m.Readers.Min || p.ReaderVersion > m.Readers.Max || p.WriterVersion < m.Writers.Min || p.WriterVersion > m.Writers.Max || len(m.Migrations) != m.DatabaseVersion {
		return ErrUnavailable
	}
	for i, v := range m.Migrations {
		if v.Version != i+1 || v.Checksum != p.Migrations[i].Checksum {
			return ErrUnavailable
		}
	}
	return nil
}
