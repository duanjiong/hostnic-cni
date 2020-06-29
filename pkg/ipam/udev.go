package ipam

import (
	"bytes"
	"fmt"
	udev "github.com/pilebones/go-udev/netlink"
	"github.com/sirupsen/logrus"
	"strconv"
	"strings"
)

type UdevNotify struct {
	Action string
	MAC    string
	index  int    //unused
	name   string //unused
}

func SplitSubN(s string, n int) []string {
	sub := ""
	subs := []string{}

	runes := bytes.Runes([]byte(s))
	l := len(runes)
	for i, r := range runes {
		sub = sub + string(r)
		if (i+1)%n == 0 {
			subs = append(subs, sub)
			sub = ""
		} else if (i + 1) == l {
			subs = append(subs, sub)
		}
	}

	return subs
}

func FormatMac(str string) string {
	str = strings.TrimPrefix(str, "enx")
	return strings.Join(SplitSubN(str, 2), ":")
}

type Udev struct {
}

type UdevFake struct {
}

func (u UdevFake) Monitor(trigCh chan UdevNotify) {

}

type UdevAPI interface {
	Monitor(trigCh chan UdevNotify)
}

// Monitor run Monitor mode
func (u Udev) Monitor(trigCh chan UdevNotify) {
	conn := new(udev.UEventConn)
	if err := conn.Connect(udev.UdevEvent); err != nil {
		panic(fmt.Sprintf("Cannot init udev: err=%+v", err))
	}
	defer conn.Close()

	var matchers udev.RuleDefinitions
	actionsAdd := "add"
	matcher := udev.RuleDefinition{
		Action: &actionsAdd,
		Env: map[string]string{
			"SUBSYSTEM": "net",
		},
	}
	matchers.AddRule(matcher)

	actionsRemove := "remove"
	matcher = udev.RuleDefinition{
		Action: &actionsRemove,
		Env: map[string]string{
			"SUBSYSTEM": "net",
		},
	}
	matchers.AddRule(matcher)

	queue := make(chan udev.UEvent)
	errors := make(chan error)
	conn.Monitor(queue, errors, &matchers)
	// Handling message from queue
	for {
		select {
		case uevent := <-queue:
			if uevent.Action == "remove" || uevent.Action == "add" {
				if uevent.Env["INTERFACE"] != "" {
					index, _ := strconv.Atoi(uevent.Env["IFINDEX"])
					trigCh <- UdevNotify{
						name:   uevent.Env["INTERFACE"],
						MAC:    FormatMac(uevent.Env["ID_NET_NAME_MAC"]),
						Action: string(uevent.Action),
						index:  index,
					}
				}
			}
		case err := <-errors:
			logrus.WithError(err).Error("IPAM udev recv error")
		}
	}
}
