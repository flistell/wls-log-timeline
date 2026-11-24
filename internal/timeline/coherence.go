package timeline

import "regexp"

type eventPattern struct {
	label   string
	pattern *regexp.Regexp
}

var coherencePatterns = []eventPattern{
	{"stopping cluster", regexp.MustCompile("(?i)\\bstopping cluster(?: service)?\\b|\\bExiting cluster\\b")},
	{"restarting cache", regexp.MustCompile("(?i)\\bRestarting NamedCache\\s*:")},
	{"Restarting cluster", regexp.MustCompile("(?i)\\bRestarting cluster\\b")},
	{"Worker STUCK", regexp.MustCompile("(?i)\\bA worker thread has been executing task:[^\\n]*\\bappears to be stuck\\b")},
	{"Handshake timeout", regexp.MustCompile("(?i)validatePolls:|unanswered handshake|timeout during service start|failed to restart services")},
	{"Storage unavailable", regexp.MustCompile("(?i)No storage-enabled nodes")},
	{"Guardian timeout", regexp.MustCompile("(?i)Detected (?:soft|hard) timeout|guard(?:ian)?[^\\n]*timeout")},
	{"Guardian recovery", regexp.MustCompile("(?i)Attempting recovery of|terminating guarded|recovering guarded")},
	{"Transport ACK timeout", regexp.MustCompile("(?i)initiating connection migration .* ack timeout|fatal ack timeout|ack timeout after")},
	{"Transport migration recovery", regexp.MustCompile("(?i)accepted connection migration|resuming migrated connection|synchronizing migrated connection")},
	{"Transport migration", regexp.MustCompile("(?i)accepting connection migration|migrating connection|rejecting connection migration")},
	{"Transport connection failure", regexp.MustCompile("(?i)Connection reset|ClosedChannelException|SocketTimeoutException|failed to identify protocol|ServerSocket failure|Error (?:connecting|establishing)")},
	{"Connection established", regexp.MustCompile("(?i)\\bConnection established with\\b")},
	{"Transport diagnostics", regexp.MustCompile("(?i)TEMP DEBUG|post-flush drain|discarding partial message|skipping control message")},
	{"Communication delay", regexp.MustCompile("(?i)communication delay|Failed to satisfy the variance")},
	{"Cluster discovery failure", regexp.MustCompile("(?i)Delaying formation of a new cluster|incompatible network configuration|older member announcing")},
	{"Cluster formation", regexp.MustCompile("(?i)Created a new cluster")},
	{"Member departure", regexp.MustCompile("(?i)left [Cc]luster|MemberLeft notification|removing the member|member .* departed")},
	{"Member join", regexp.MustCompile("(?i)joined [Cc]luster|joined Cluster")},
	{"Partition stabilized", regexp.MustCompile("(?i)Partition ownership has stabilized")},
	{"Partition transfer pending", regexp.MustCompile("(?i)partition distribution has been pending")},
	{"Partition recovery", regexp.MustCompile("(?i)orphaned|orphan partitions|Restored from backup|recover(?:ed|ing) partition")},
	{"Partition transfer", regexp.MustCompile("(?i)Transferring .*PartitionSet|DistributionRequest|[Rr]e-publishing the ownership")},
	{"Service departure", regexp.MustCompile("(?i)left service|Service .* left the cluster")},
	{"Service join", regexp.MustCompile("(?i)joined service|Service .* joined the cluster")},
	{"Service lifecycle", regexp.MustCompile("(?i)Service .* (?:started|stopped)|Starting service|Stopping service")},
	{"Proxy request failure", regexp.MustCompile("(?i)exception occurred while processing .*Request")},
	{"Protocol assertion", regexp.MustCompile("(?i)Assertion failed")},
	{"Management failure", regexp.MustCompile("(?i)Failure to deserialize an Invocable|unsupported JMX service URL")},
}
