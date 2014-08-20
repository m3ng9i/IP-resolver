/*
IP resolver
*/
package main

import "fmt"
import "errors"
import "os"
import "io/ioutil"
import "encoding/json"
import "time"
import "strings"
import "sort"
import "sync"
import "flag"
import "path/filepath"

import mdns "github.com/miekg/dns"

const version = "v0.1-13"

type DnsAddr struct {
    Name string
    Address string
}

type DnsAddrs []DnsAddr

type Answer struct {
    DnsAddr
    IP []string
    Error error
}

type Answers []Answer

type AnswerJson struct {
    DnsAddr
    IP []string
    Error string
}

type AnswersJson []AnswerJson

type ReadConfigError struct {
    Path string
    Errmsg string
    Exit bool
}

func (e *ReadConfigError) Error() string {
    return fmt.Sprintf("Configuration file '%s' load error: %s", e.Path, e.Errmsg)
}

var appname string

/* from qlibgo/dns

Get a domain's IPs from a specific name server.

Parameters:
    domain      the domain you want to query
    nameserver  name server's IP address
    port        53 in general
    net         tcp or udp
    timeout     in seconds, can be omitted

Here's an example：
    r, e := ARecords("www.example.com", "8.8.8.8", 53, "tcp")
    if e != nil {
        fmt.Println(e)
    } else {
        fmt.Println(r)
    }
*/
func ARecords(domain, nameserver string, port uint16, net string, timeout ...uint8) ([]string, error) {
    var result []string

    if net != "tcp" && net != "udp" {
        return result, errors.New("The Parameter 'net' should only be 'tcp' or 'udp'.")
    }

    msg := new(mdns.Msg)
    msg.SetQuestion(mdns.Fqdn(domain), mdns.TypeA)

    var client *mdns.Client
    if len(timeout) > 0 {
        tm := time.Duration(timeout[0]) * time.Second
        client = &mdns.Client { Net: net, DialTimeout: tm, ReadTimeout: tm, WriteTimeout: tm }
    } else {
        client = &mdns.Client { Net: net }
    }

    r, _, err := client.Exchange(msg, fmt.Sprintf("%s:%d", nameserver, port))
    if err != nil {
        return result, err
    }

    for _, i := range r.Answer {
        if t, ok := i.(*mdns.A); ok {
            result = append(result, t.A.String())
        }
    }

    return result, nil
}


/*
Use goroutines to query one domain with multiple name servers.

Parameters:
    dns     name server configuration
    domain  the domain you want to query
    net     tcp or udp
*/
func query(dns DnsAddrs, domain string, net string) Answers {
    var wg sync.WaitGroup
    answers := make(Answers, len(dns))
    for j, i := range dns {
        wg.Add(1)
        go func(n int, d DnsAddr) {
            defer wg.Done()
            var answer Answer
            answer.DnsAddr = d
            ip, err := ARecords(domain, d.Address, 53, net, 3)
            if err != nil {
                answer.Error = err
            } else {
                if len(ip) == 0 {
                    answer.Error = errors.New("No result")
                } else {
                    answer.IP = ip
                }
            }
            answers[n] = answer
        }(j, i)
    }
    
    wg.Wait()
    return answers
}


// Get all the IPs from the query results.
func (a Answers) allIP() []string {

    var ips []string
    i := make(map[string]bool)

    for _, item := range a {
        for _, ip := range item.IP {
            i[ip] = true
        }
    }

    for key, _ := range i {
        ips = append(ips, key)
    }

    sort.Strings(ips)
    return ips

}


func in(ip string, ips []string) bool {
    for _, i := range ips {
        if i == ip {
            return true
        }
    }
    return false
}


// Output the query results.
func (a Answers) output() {

    allip := a.allIP()

    resultNum := len(allip)
    if resultNum == 0 {
        resultNum = 1 // leave room for displaying error
    }

    /*
    First line is name servers's names, the second line is theirs IPs. Example:
    DNS1         DNS2         DNS3         DNS4
    1.1.1.1      2.2.2.2      3.3.3.3      4.4.4.4
    */
    head := make([]string, len(a) * 2)

    /*
    A domain's IPs queried from different name servers. Example:
    11.11.11.11  Timout       -            -
    11.11.11.12  -            11.11.11.12  -
    -            -            11.11.11.13  -
    -            -            -            11.11.11.14
    */
    ip := make([]string, len(a) * resultNum)

    // Fill ip with "-" 
    for i, _ := range ip {
        ip[i] = "-"
    }

    // Fill head and ip
    for i, item:= range a {
        head[i]         = item.Name
        head[i+len(a)]  = item.Address

        if item.Error == nil {
            for j:=0; j<len(allip); j++ {
                if in(allip[j], item.IP) {
                    ip[j * len(a) + i] = allip[j]
                }
            }
        } else {
            ip[i] = errToString(item.Error)
        }
    }

    // Output variable "head" and "ip" to stdout.
    // The max length of IP string is 15, plus two space is 17, that's the reason I use "17" in Printf.

    for i, item := range head {
        fmt.Printf("%-17.16s", item)
        if (i + 1) % len(a) == 0 {
            fmt.Println()
        }
    }

    fmt.Println(strings.Repeat("-", 17 * len(a)))

    for i, item := range ip {
        fmt.Printf("%-17s", item)
        if (i + 1) % len(a) == 0 {
            fmt.Println()
        }
    }
}


// Output all IPs resolved from all nameserver and ignore errors.
func (a Answers) outputNormal() {

    allip := a.allIP()

    for _, i := range allip {
        fmt.Println(i)
    }
}



// Output Json with full error message.
func (a Answers) outputJson() {

    aj := make(AnswersJson, len(a))
    for j, item := range(a) {
        aj[j].DnsAddr = item.DnsAddr
        aj[j].IP = item.IP
        if item.Error != nil {
            aj[j].Error = item.Error.Error()
        }
    }

    b, err := json.Marshal(aj)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error occurred when generating json: %s\n", err.Error())
        os.Exit(1)
    }
    fmt.Println(string(b))
}


// Convert error message to a short form.
func errToString(err error) string {
    if err == nil {
        return ""
    }
    
    var f = func(str, substr string) bool {
        return strings.Contains(strings.ToLower(str), substr)
    }

    s := err.Error()

    if f(s, "timeout") {
        // Errors like "dial tcp 8.8.8.8:53: ConnectEx tcp: i/o timeout" 
        //   or "WSARecv tcp 192.168.0.1:3586: i/o timeout"
        return "Timeout"

    } else if f(s, "refused the network connection") {
        // Errors like "dial tcp 8.8.8.8:53: ConnectEx tcp: 
        //   The remote system refused the network connection."
        // When this error came up after you send a tcp query, this maybe means
        //   the nameserver doesn't support tcp protocol, so choose udp instead.
        return "Conn refused"

    } else if f(s, "no service is operating") {
        // Errors like "WSARecv udp 192.168.0.1:1573: No service is operating at
        //   the destination network endpoint on the remote system."
        return "NS invalid"

    } else if f(s, "forcibly closed by the remote host") {
        // Errors like "WSARecv udp 192.168.0.1:1590: An existing connection
        //   was forcibly closed by the remote host.
        return "NS invalid"

    } else if f(s, "no result") {
        return "No result"

    }

    // Deal with other error message
    return "Connect error"

}


func usage(stderr bool) func() {
s := `IP resolver

IP resolver is a command-line tool for getting a domain's IPs from multiple name
servers. It can show different query results between different name servers. 
This tool is implemented in Go.

Usage:
    appname [-l <file] [-f <std|json|ip>] [-t] <domain>
    appname [-l <file] -c
    appname -s
    appname -h
    appname -v

Options:
    -l, -load <file>            Use <file> instead of default configuration file
    -f, -format <std|json|ip>   Specify the output format
    -s, -sample                 Output sample configuration to stdout
    -t, -tcp                    Use tcp protocol instead of udp
    -c, -config                 Print content of configuration file
    -h, -help                   Show help
    -v, -version                Output version information

Output format:
    std                         Default output format, display each name server
                                and it's query results.
    json                        Like default format, but in JSON.    
    ip                          Only show IPs, not include name server's info.
   
Configuration file:    
    The configuration file is JSON formatted. Use "-s" to see a example.
    If "-l <file>" is not given, the file is searched in the following order:
    1. ~/.config/ipre.conf
    2. ~/.ipre
    3. /etc/ipre.conf

Example:
    appname www.example.com
    appname -l config.json -f json -tcp www.example.com
    appname -l config.json -c
    appname -s > ~/.ipre && appname www.example.com

Author:    
    mengqi <5b5f7426@gmail.com>
    https://github.com/m3ng9i`

    s = strings.Replace(s, "appname", appname, -1)

    return func() {
        if stderr {
            fmt.Fprintln(os.Stderr, s)
        } else {
            fmt.Println(s)
        }
    }

}


// configuration sample
func writeSample() {
j := `[
    {"name": "AliDNS",    "address": "223.5.5.5"        },
    {"name": "114DNS",    "address": "114.114.114.114"  },
    {"name": "Google",    "address": "8.8.8.8"          },
    {"name": "OpenDNS",   "address": "208.67.222.222"   }
]`

fmt.Println(j)
    
}


// Read namserver configuration from a JSON file
// If there is an error, it will be of type *ReadConfigError
func readConfig(file string) (DnsAddrs, error) {
    var conf DnsAddrs

    stat, err := os.Stat(file)
    if os.IsNotExist(err) {
        return conf, &ReadConfigError{
                        Path:file, 
                        Errmsg:"file does not exists", 
                        Exit:false}
    }
    if stat.IsDir() {
        return conf, &ReadConfigError{
                        Path:file, 
                        Errmsg:"file is directory", 
                        Exit:true}
    }

    b, err := ioutil.ReadFile(file)
    if err != nil {
        return conf, &ReadConfigError{
                        Path:file, 
                        Errmsg:err.Error(), 
                        Exit:true}
    }

    json.Unmarshal(b, &conf)

    if len(conf) == 0 {
        return conf, &ReadConfigError{
                        Path:file, 
                        Errmsg:"file format is not correct, use '-s' to see a sample configuration", 
                        Exit:true}
    }

    return conf, nil

}



/*
Try to read configuration file

Configuration file is searched in the following order:
    1. ~/.config/ipre.conf
    2. ~/.ipre
    3. /etc/ipre.conf

Return value:
    DnsAddrs    dns configuration
    path        path of configuration file
    error
*/
func getDefaultConfig() (DnsAddrs, string, error) {

    const configFile = "ipre.conf"
    const configFileH = ".ipre"

    var paths []string
    var err error
    var conf DnsAddrs

    home := os.Getenv("HOME") // unix
    if home == "" {
        home = filepath.Join(os.Getenv("HOMEDRIVE"), os.Getenv("HOMEPATH")) // windows
    }

    if home != "" {
        paths = append(paths, filepath.Join(home, ".config", configFile))
        paths = append(paths, filepath.Join(home, configFileH))
    }
    paths = append(paths, filepath.Join("/etc", configFile))

    for _, item := range(paths) {
        conf, err = readConfig(item)

        if err == nil {
            return conf, item, nil
        }

        if e, ok := err.(*ReadConfigError); ok {
            if e != nil {
                if e.Exit {
                    return conf, item, e
                }
            } else {
                return conf, item, nil
            }
        } 
    } 

    return conf, "", errors.New("Configuration file not found")
}


func main() {

    var configfile, format string
    var sample, useTcp, printconf, help, showver bool

    flag.StringVar(&configfile, "l", "", "-l <file>")
    flag.StringVar(&configfile, "load", "", "-load <file>")
    flag.StringVar(&format, "f", "", "-f <std|json|ip>")
    flag.StringVar(&format, "format", "", "-format <std|json|ip>")
    flag.BoolVar(&sample, "s", false, "-s")
    flag.BoolVar(&sample, "sample", false, "-sample")
    flag.BoolVar(&useTcp, "t", false, "-t")
    flag.BoolVar(&useTcp, "tcp", false, "-tcp")
    flag.BoolVar(&printconf, "c", false, "-c")
    flag.BoolVar(&printconf, "config", false, "-config")
    flag.BoolVar(&help, "help", false, "-help")
    flag.BoolVar(&help, "h", false, "-h")
    flag.BoolVar(&showver, "v", false, "-v")
    flag.BoolVar(&showver, "version", false, "-v")

    if len(os.Args) > 0 {
        appname = filepath.Base(os.Args[0])
    } else {
        appname = "ipre"
    }

    flag.Usage = usage(true)
    flag.Parse()

    if help {
        usage(false)()
        os.Exit(0)
    }

    if sample {
        writeSample()
        os.Exit(0)
    }

    if showver {
        fmt.Printf("IP resolver %s\n", version)
        os.Exit(0)
    }

    if format == "" {
        format = "std"
    } else if format != "std" && format != "json" && format != "ip" {
        fmt.Fprintf(os.Stderr, "Format %s is not correct, use '-h' for help\n", format)
        os.Exit(1)
    }

    var conf DnsAddrs
    var confpath string
    var err error
    if configfile == "" {
        // read default configuration file
        conf, confpath, err = getDefaultConfig()
        if err != nil {
            fmt.Fprintln(os.Stderr, err.Error())
            os.Exit(1)
        }
    } else {
        conf, err = readConfig(configfile)

        if err != nil {
            fmt.Fprintln(os.Stderr, err.Error())
            os.Exit(1)
        }
        confpath = configfile
    }

    if printconf {
        fmt.Println(confpath)
        b, err := json.Marshal(conf)
        if err != nil {
            fmt.Fprintf(os.Stderr, "Error occurred when generating json: %s\n", err.Error())
            os.Exit(1)
        }
        fmt.Println(string(b))
        os.Exit(0)
    }

    if len(flag.Args()) == 0 {
        fmt.Fprintln(os.Stderr, "Please input a domain for querying, use '-h' for help")
        os.Exit(1)
    }

    var net string
    if useTcp {
        net = "tcp"
    } else {
        net = "udp"
    }
    
    result := query(conf, flag.Args()[0], net)
    if format == "std" {
        result.output()
    } else if format == "json" {
        result.outputJson()
    } else if format == "ip" {
        result.outputNormal()
    }

}
