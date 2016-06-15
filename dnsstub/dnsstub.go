package dnsstub

import (
	"math/big"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"time"
	"github.com/miekg/dns"
)

type query struct {
	handle	int		// identifier to match answer with question
	qname	string
	rtype	uint16
}

type answer struct {
	handle	int		// identifier to match answer with question
	qname	string
	rtype	uint16
	answer	*dns.Msg
	rtt	time.Duration
	err	error
}

type StubResolver struct {
	next_handle		int
	queries			chan *query
	answers			chan *answer
	finished_answers	[]*answer
}

func RandUint16() (uint16, error) {
	var id_max big.Int
	id_max.SetUint64(65536)
	id, err := rand.Int(rand.Reader, &id_max)
	if err != nil {
		return 0, err
	}
	return uint16(id.Uint64()), nil
}

/*
   Send a query to a DNS server, retrying and handling truncation.
 */
func DnsQuery(server string, query *dns.Msg) (*dns.Msg, time.Duration, error) {
	// try to query first in UDP
	dnsClient := new(dns.Client)
	id, err := RandUint16()
	if err != nil {
		return nil, 0, err
	}
	query.Id = id
	r, rtt, err := dnsClient.Exchange(query, server)
	if (err != nil) && (err != dns.ErrTruncated) {
		return nil, 0, err
	}
	if (r.Rcode == dns.RcodeSuccess) && !r.Truncated {
		return r, rtt, nil
	}
	// if this didn't work, try again in TCP
	dnsClient.Net = "tcp"
	r, rtt, err = dnsClient.Exchange(query, server)
	if err != nil {
		return nil, 0, err
	}
	// return whatever we get in this case, even if an erroneous response
	return r, rtt, nil
}

func stub_resolve(servers []string, queries <-chan *query, answers chan<- *answer) {
	for q := range queries {
		dns_query := new(dns.Msg)
		dns_query.RecursionDesired = true
		dns_query.SetQuestion(q.qname, q.rtype)
		a := new(answer)
		a.handle = q.handle
		a.qname = q.qname
		a.rtype = q.rtype
		a.answer = nil
		for _, server := range servers {
			// look for ':' because that indicates an IPv6 address
			var resolver string
			if strings.ContainsRune(server, ':') {
				resolver = "[" + server + "]:53"
			} else {
				resolver = server + ":53"
			}
			a.answer, a.rtt, a.err = DnsQuery(resolver, dns_query)
			if a.answer != nil {
				break
			}
		}
		answers <- a
	}
}

func Init(concurrency int, server_ips []net.IP) (resolver *StubResolver, err error) {
	stub := new(StubResolver)
	servers := make([]string, 0, 0)
	for _, ip := range server_ips {
		servers = append(servers, ip.String())
	}
	if len(servers) == 0 {
		resolv_conf, err := dns.ClientConfigFromFile("/etc/resolv.conf")
		if err != nil {
			newerr := fmt.Errorf("error reading resolver configuration from '/etc/resolv.conf'; %s", err)
			return nil, newerr
		}
		servers = resolv_conf.Servers
	}
	stub.queries = make(chan *query, concurrency * 4)
	stub.answers = make(chan *answer, concurrency * 2)
	for i := 0; i < concurrency; i++ {
		go stub_resolve(servers, stub.queries, stub.answers)
	}
	return stub, nil
}

func (resolver *StubResolver) Query(qname string, rtype uint16) (handle int) {
	q := new(query)
	resolver.next_handle += 1
	q.handle = resolver.next_handle
	q.qname = qname
	q.rtype = rtype
	resolver.queries <- q
	return q.handle
}

func (resolver *StubResolver) Wait() (*dns.Msg, time.Duration, string, uint16, error) {
	var a *answer
	// if we have waiting finished answers, return one of them
	if len(resolver.finished_answers) > 0 {
		a = resolver.finished_answers[0]
		resolver.finished_answers = resolver.finished_answers[1:]
	// otherwise wait for an answer to arrive
	} else {
		a = <-resolver.answers
	}
	return a.answer, a.rtt, a.qname, a.rtype, a.err
}

func (resolver *StubResolver) WaitByHandle(handle int) (*dns.Msg, time.Duration, string, uint16, error) {
	// check any existing finished answers to see if we have ours
	for n, a := range resolver.finished_answers {
		if a.handle == handle {
			resolver.finished_answers = append(resolver.finished_answers[:n], resolver.finished_answers[n+1:]...)
			return a.answer, a.rtt, a.qname, a.rtype, a.err
		}
	}
	for {
		a := <-resolver.answers
		if a.handle == handle {
			return a.answer, a.rtt, a.qname, a.rtype, a.err
		}
		resolver.finished_answers = append(resolver.finished_answers, a)
	}
}

func (resolver *StubResolver) Close() {
	close(resolver.queries)
	close(resolver.answers)
}

/*
func main() {
	resolver, err := Init(11, nil)
	if err != nil {
		fmt.Printf("Error! %s\n", err)
		return
	}
	resolver.Query("isc.org.", dns.TypeA)
	sleep_time, _ := time.ParseDuration("1s")
	time.Sleep(sleep_time)	// insure that our non-handle query finishes first
	handle := resolver.Query("isc.org.", dns.TypeAAAA)
	answer, _, _, err := resolver.WaitByHandle(handle)
	fmt.Printf("answer: %s\n", answer)
	answer, _, _, err = resolver.Wait()
	fmt.Printf("answer: %s\n", answer)
	resolver.Close()
}
*/
