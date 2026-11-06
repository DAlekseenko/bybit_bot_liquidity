package storage

import (
	"errors"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

// ErrPostgresConfig сообщает о недопустимой локальной конфигурации PostgreSQL.
var ErrPostgresConfig = errors.New("invalid postgres configuration")

// localPostgresConfig строит явную конфигурацию pq только из переданных значений.
func localPostgresConfig(dsn, pqDebug string) (pq.Config, error) {
	if pqDebug == "1" || strings.Contains(dsn, "#") {
		return pq.Config{}, ErrPostgresConfig
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Opaque != "" || u.Fragment != "" || u.User == nil {
		return pq.Config{}, ErrPostgresConfig
	}
	user := u.User.Username()
	password, passwordSet := u.User.Password()
	if user == "" || password == "" || !passwordSet {
		return pq.Config{}, ErrPostgresConfig
	}
	host, portText, err := splitPostgresAuthority(u.Host)
	if err != nil {
		return pq.Config{}, ErrPostgresConfig
	}
	address, err := netip.ParseAddr(host)
	port, portErr := strconv.ParseUint(portText, 10, 16)
	if err != nil || (strings.HasPrefix(u.Host, "[") && !address.Is6()) || address.Zone() != "" || !address.IsLoopback() || portErr != nil || port == 0 {
		return pq.Config{}, ErrPostgresConfig
	}
	path := u.EscapedPath()
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] != "" || parts[1] == "" {
		return pq.Config{}, ErrPostgresConfig
	}
	database, err := url.PathUnescape(parts[1])
	if err != nil || database == "" || strings.Contains(database, "/") {
		return pq.Config{}, ErrPostgresConfig
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || u.RawQuery != "sslmode=disable" || len(query) != 1 || len(query["sslmode"]) != 1 || query.Get("sslmode") != "disable" {
		return pq.Config{}, ErrPostgresConfig
	}
	return pq.Config{
		Host: host, Hostaddr: address, Port: uint16(port), Database: database,
		User: user, Password: password, SSLMode: pq.SSLModeDisable,
		MinProtocolVersion: pq.ProtocolVersion("3.0"), MaxProtocolVersion: pq.ProtocolVersion("3.0"),
		ClientEncoding: "UTF8", Datestyle: "ISO, MDY", TZ: "UTC", ApplicationName: "momentum",
		ConnectTimeout: 5 * time.Second, Options: "-c search_path=pg_catalog,momentum -c timezone=UTC",
	}, nil
}

// splitPostgresAuthority отделяет literal host и обязательный порт без DNS.
func splitPostgresAuthority(authority string) (string, string, error) {
	if strings.Contains(authority, ",") {
		return "", "", ErrPostgresConfig
	}
	if strings.HasPrefix(authority, "[") {
		end := strings.IndexByte(authority, ']')
		if end < 0 || end+1 >= len(authority) || authority[end+1] != ':' {
			return "", "", ErrPostgresConfig
		}
		return authority[1:end], authority[end+2:], nil
	}
	if strings.Count(authority, ":") != 1 {
		return "", "", ErrPostgresConfig
	}
	parts := strings.SplitN(authority, ":", 2)
	return parts[0], parts[1], nil
}
