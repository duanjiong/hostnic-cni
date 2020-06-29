package networkutils

import (
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"net"
	"syscall"
)

// containsNoSuchRule report whether the rule is not exist
func containsNoSuchRule(err error) bool {
	if errno, ok := err.(syscall.Errno); ok {
		return errno == syscall.ENOENT
	}
	return false
}

// isRuleExistsError report whether the rule is exist
func isRuleExistsError(err error) bool {
	if errno, ok := err.(syscall.Errno); ok {
		return errno == syscall.EEXIST
	}
	return false
}


// isNotExistsError returns true if the error type is syscall.ESRCH
// This helps us determine if we should ignore this error as the route
// that we want to cleanup has been deleted already routing table
func isNotExistsError(err error) bool {
	if errno, ok := err.(syscall.Errno); ok {
		return errno == syscall.ENOENT
	}
	return false
}

// isRouteExistsError returns true if the error type is syscall.EEXIST
// This helps us determine if we should ignore this error as the route
// we want to add has been added already in routing table
func isRouteExistsError(err error) bool {
	if errno, ok := err.(syscall.Errno); ok {
		return errno == syscall.EEXIST
	}
	return false
}

// isNetworkUnreachableError returns true if the error type is syscall.ENETUNREACH
// This helps us determine if we should ignore this error as the route the call
// depends on is not plumbed ready yet
func isNetworkUnreachableError(err error) bool {
	if errno, ok := err.(syscall.Errno); ok {
		return errno == syscall.ENETUNREACH
	}
	return false
}


func getRuleListBySrc(src net.IPNet) ([]netlink.Rule, error) {
	var srcRuleList []netlink.Rule
	ruleList, err := netlink.RuleList(unix.AF_INET)
	if err != nil {
		return nil, err
	}
	for _, rule := range ruleList {
		if rule.Src != nil && rule.Src.IP.Equal(src.IP) {
			srcRuleList = append(srcRuleList, rule)
		}
	}
	return srcRuleList, nil
}
