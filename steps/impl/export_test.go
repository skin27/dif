package impl

import "testing"

// The fake servers of the tests in this package, for the flow tests of
// remote_flow_test.go, which are in package impl_test as they use the api.

// FakeFTPForTest starts an FTP server on a temporary directory.
func FakeFTPForTest(t *testing.T) (host, root, user, password string) {
	e := newFTPEnv(t, nil)
	return e.host, e.root, e.login["userName"].(string), e.login["password"].(string)
}

// FakeSFTPForTest starts an SFTP server on a temporary directory.
func FakeSFTPForTest(t *testing.T) (host, root, user, password string) {
	e := newSFTPEnv(t)
	return e.host, e.root, e.login["userName"].(string), e.login["password"].(string)
}
