package impl

import stepdef "dif/steps/definition"

// How the steps take a typed connection (dil.core.connections, with the
// option connection): which of their options each field of the connection
// sets. An option the step sets itself wins.

// trustStore maps the trust store of a connection's tls.
var trustStore = map[string]string{
	"tls.trustStoreFile":     "trustStoreFile",
	"tls.trustStorePassword": "trustStorePassword",
}

// connectionOptions returns the options of a mapping: the given ones and,
// for each of more, its options too.
func connectionOptions(options map[string]string, more ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range append([]map[string]string{options}, more...) {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

var (
	sftpConnection = &stepdef.ConnectionMapping{
		Kinds:   []string{"sftp"},
		Address: "path",
		Options: map[string]string{
			"authentication.username":   "userName",
			"authentication.password":   "password",
			"authentication.privateKey": "privateKey",
			"authentication.passphrase": "privateKeyPassphrase",
			"hostKeys.knownHostsFile":   "knownHostsFile",
			"hostKeys.strict":           "strictHostKeyChecking",
			"socketTimeout":             "socketTimeout",
		},
	}
	ftpsConnection = &stepdef.ConnectionMapping{
		Kinds:   []string{"ftps"},
		Address: "path",
		Options: connectionOptions(map[string]string{
			"authentication.username": "userName",
			"authentication.password": "password",
			"tls.implicit":            "implicit",
			"socketTimeout":           "socketTimeout",
		}, trustStore),
	}
	smbConnection = &stepdef.ConnectionMapping{
		Kinds:   []string{"smb"},
		Address: "path",
		Options: map[string]string{
			"authentication.username": "userName",
			"authentication.password": "password",
			"authentication.domain":   "domain",
			"socketTimeout":           "socketTimeout",
		},
	}
	httpsConnection = &stepdef.ConnectionMapping{
		Kinds:   []string{"https"},
		Address: "path",
		Options: connectionOptions(map[string]string{
			"authentication.username": "authUsername",
			"authentication.password": "authPassword",
			"connectTimeout":          "connectTimeout",
			"socketTimeout":           "socketTimeout",
		}, trustStore),
		Implied: map[string]map[string]any{"authentication.username": {"authMethod": "Basic"}},
	}
	smtpConnection = &stepdef.ConnectionMapping{
		Kinds:   []string{"smtp"},
		Address: "path",
		Options: connectionOptions(map[string]string{
			"authentication.username": "username",
			"authentication.password": "password",
			"authentication.token":    "accessToken",
			"socketTimeout":           "timeout",
		}, trustStore),
	}
	imapsConnection = &stepdef.ConnectionMapping{
		Kinds:   []string{"imaps"},
		Address: "path",
		Options: connectionOptions(map[string]string{
			"authentication.username": "username",
			"authentication.password": "password",
			"authentication.token":    "accessToken",
			"socketTimeout":           "socketTimeout",
		}, trustStore),
		Implied: map[string]map[string]any{"authentication.token": {"authenticationType": "oauth"}},
	}
	as2Connection = &stepdef.ConnectionMapping{
		Kinds: []string{"as2"},
		Options: connectionOptions(map[string]string{
			"host":           "hostName",
			"port":           "targetPortNumber",
			"tls.enabled":    "",
			"connectTimeout": "connectTimeout",
			"socketTimeout":  "socketTimeout",
		}, trustStore),
		// Any sslContext selects https.
		Implied: map[string]map[string]any{
			"tls.enabled":            {"sslContext": "default"},
			"tls.trustStoreFile":     {"sslContext": "default"},
			"tls.trustStorePassword": {"sslContext": "default"},
		},
	}
	// sqlTLS maps what both sql steps take of a sql connection.
	sqlTLS = connectionOptions(map[string]string{
		"tls.enabled":    "useSSL",
		"tls.version":    "tlsVersion",
		"connectTimeout": "connectTimeout",
		"socketTimeout":  "socketTimeout",
	}, trustStore)
	sqlConnection = &stepdef.ConnectionMapping{
		Kinds: []string{"sql"},
		Options: connectionOptions(map[string]string{
			"dialect":                 "connectionType",
			"host":                    "host",
			"port":                    "port",
			"database":                "database",
			"authentication.username": "username",
			"authentication.password": "password",
		}, sqlTLS),
	}
	// The loader gives sql2 the address and authentication of a sql
	// connection as its option connection; the rest goes through here.
	sql2Connection = &stepdef.ConnectionMapping{Kinds: []string{"sql"}, Options: sqlTLS}
)

// stepConnections are the steps that take a typed connection, by name and kind.
var stepConnections = map[string]*stepdef.ConnectionMapping{
	"sftp/" + stepdef.Source:       sftpConnection,
	"sftp/" + stepdef.Sink:         sftpConnection,
	"sftpenrich/" + stepdef.Action: sftpConnection,
	"ftps/" + stepdef.Source:       ftpsConnection,
	"ftps/" + stepdef.Sink:         ftpsConnection,
	"ftpsenrich/" + stepdef.Action: ftpsConnection,
	"smb/" + stepdef.Source:        smbConnection,
	"smb/" + stepdef.Sink:          smbConnection,
	"smb/" + stepdef.Action:        smbConnection,
	"smbenrich/" + stepdef.Action:  smbConnection,
	"https/" + stepdef.Action:      httpsConnection,
	"smtp/" + stepdef.Action:       smtpConnection,
	"smtps/" + stepdef.Action:      smtpConnection,
	"imaps/" + stepdef.Source:      imapsConnection,
	"as2/" + stepdef.Action:        as2Connection,
	"sql/" + stepdef.Action:        sqlConnection,
	"sql2/" + stepdef.Action:       sql2Connection,
}
