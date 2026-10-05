package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/farero-dev/farero/daemon/internal/model"
)

// PolicyOverrides returns the user's per-tool level overrides keyed by
// "<plugin>_<tool>".
func (s *Store) PolicyOverrides(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tool_key, level FROM policy_overrides`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetPolicyOverride stores an override; an empty level removes it.
func (s *Store) SetPolicyOverride(ctx context.Context, toolKey, level string) error {
	if level == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM policy_overrides WHERE tool_key = ?`, toolKey)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO policy_overrides(tool_key, level) VALUES(?, ?)
		 ON CONFLICT(tool_key) DO UPDATE SET level = excluded.level`, toolKey, level)
	return err
}

// Plugin returns a plugin's stored state (status "disconnected" if absent).
func (s *Store) Plugin(ctx context.Context, name string) (model.PluginState, error) {
	var p model.PluginState
	var connected int64
	var opts string
	err := s.db.QueryRowContext(ctx,
		`SELECT plugin, status, account_label, connected_at, options FROM plugins WHERE plugin = ?`, name).
		Scan(&p.Plugin, &p.Status, &p.AccountLabel, &connected, &opts)
	if errors.Is(err, sql.ErrNoRows) {
		return model.PluginState{Plugin: name, Status: model.PluginDisconnected}, nil
	}
	if err != nil {
		return p, err
	}
	p.ConnectedAt = fromMs(connected)
	_ = json.Unmarshal([]byte(opts), &p.Options)
	return p, nil
}

// SavePlugin stores a plugin's state. Tokens are never stored here.
func (s *Store) SavePlugin(ctx context.Context, p model.PluginState) error {
	opts, _ := json.Marshal(p.Options)
	if p.Options == nil {
		opts = []byte("{}")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO plugins(plugin, status, account_label, connected_at, options) VALUES(?,?,?,?,?)
ON CONFLICT(plugin) DO UPDATE SET status = excluded.status, account_label = excluded.account_label,
	connected_at = excluded.connected_at, options = excluded.options`,
		p.Plugin, p.Status, p.AccountLabel, ms(p.ConnectedAt), string(opts))
	return err
}
