package ovnstatus

import (
	"fmt"
	"testing"
	"time"
)

var testStdout = `` +
	`Last Election started 259684608 ms ago, reason: leadership_transfer
Last Election won: 259684604 ms ago
Election timer: 5000
Log: [20946, 20968]
Entries not yet committed: 0
Entries not yet applied: 0
Connections: ->7bdb ->b007 <-7bdb <-b007
Disconnections: 34130
Servers:
    e40d (e40d at ssl:[192.168.100.12]:6643) (self)
    7bdb (7bdb at ssl:[192.168.100.11]:6643) last msg 425139 ms ago
    b007 (b007 at ssl:[192.168.100.14]:6643) last msg 817 ms ago
`
var expectedServersBlock = `` +
	`    e40d (e40d at ssl:[192.168.100.12]:6643) (self)
    7bdb (7bdb at ssl:[192.168.100.11]:6643) last msg 425139 ms ago
    b007 (b007 at ssl:[192.168.100.14]:6643) last msg 817 ms ago
`

func TestExtractServersBlock(t *testing.T) {
	if actual := extractServersBlock(testStdout); actual != expectedServersBlock {
		fmt.Println([]byte(actual))
		fmt.Println([]byte(expectedServersBlock))
		t.Errorf("error extracting servers block from following string:\n%s\nexpected:\n%s\ngot:\n%s\n", testStdout, expectedServersBlock, actual)
	}
}

func TestParseServersBlock(t *testing.T) {
	cs := parseServersFromTextWithThreshold(testStdout, 10*time.Second)
	fmt.Printf("%+v\n", cs)
}
