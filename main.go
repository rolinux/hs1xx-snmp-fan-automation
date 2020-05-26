/*
Automation tool using several technologies:
- switch temperature using snmp (it can be adapted for other devices)
- TP-Link Smart Wi-Fi Plug - HS100 (probably works HS110)
- a Fan connected to the Plug to be turned on if the temperature is too high
- a Prometheus exporter to graph the switch temperature

Target switch is my https://www.ui.com/unifi-switching/unifi-switch-8-150w/
and the OID is 1.3.6.1.4.1.4413.1.1.43.1.8.1.5.1.1

*/

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	// "github.com/soniah/gosnmp" v1.26.0+ needs crypto libraries
	// _ "crypto/aes"
	// _ "crypto/sha256"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rolinux/hs1xxplug"
	"github.com/soniah/gosnmp"
)

var (
	snmpTemp = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "switch_temperature",
		Help: "Current temperature of the switch",
	})

	hs1xxRelayState = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "hs1xx_relay_state",
		Help: "Plug On or Off state",
	})
)

type hs100 struct {
	System struct {
		GetSysinfo struct {
			ErrCode    int     `json:"err_code"`
			SwVer      string  `json:"sw_ver"`
			HwVer      string  `json:"hw_ver"`
			Type       string  `json:"type"`
			Model      string  `json:"model"`
			Mac        string  `json:"mac"`
			DeviceID   string  `json:"deviceId"`
			HwID       string  `json:"hwId"`
			FwID       string  `json:"fwId"`
			OemID      string  `json:"oemId"`
			Alias      string  `json:"alias"`
			DevName    string  `json:"dev_name"`
			IconHash   string  `json:"icon_hash"`
			RelayState int     `json:"relay_state"`
			OnTime     int     `json:"on_time"`
			ActiveMode string  `json:"active_mode"`
			Feature    string  `json:"feature"`
			Updating   int     `json:"updating"`
			Rssi       int     `json:"rssi"`
			LedOff     int     `json:"led_off"`
			Latitude   float64 `json:"latitude"`
			Longitude  float64 `json:"longitude"`
		} `json:"get_sysinfo"`
	} `json:"system"`
}

// helper function to get environment variables or return error
func getEnv(key string) (string, error) {
	if value, ok := os.LookupEnv(key); ok {
		return value, nil
	}
	return "", fmt.Errorf("%s environment variable not set", key)
}

func getTemperature(switchIP, snmpUsername, snmpPassword, temperatureOID string) int {
	// build our own GoSNMP struct, rather than using gosnmp.Default
	params := &gosnmp.GoSNMP{
		Target:        switchIP,
		Port:          161,
		Version:       gosnmp.Version3,
		Timeout:       time.Duration(30) * time.Second,
		SecurityModel: gosnmp.UserSecurityModel,
		MsgFlags:      gosnmp.AuthPriv,
		SecurityParameters: &gosnmp.UsmSecurityParameters{UserName: snmpUsername,
			AuthenticationProtocol:   gosnmp.SHA,
			AuthenticationPassphrase: snmpPassword,
			PrivacyProtocol:          gosnmp.AES,
			PrivacyPassphrase:        snmpPassword,
		},
	}
	err := params.Connect()
	if err != nil {
		log.Fatalf("Connect() err: %v", err)
	}
	defer params.Conn.Close()

	oids := []string{temperatureOID}
	result, err2 := params.Get(oids) // Get() accepts up to gosnmp.MAX_OIDS
	if err2 != nil {
		log.Fatalf("Get() err: %v", err2)
	}

	for _, variable := range result.Variables {
		return variable.Value.(int)
	}
	return 0
}

func getSocketOnUptime(hs1xxSocketIP string) int {
	plug := hs1xxplug.Hs1xxPlug{IPAddress: hs1xxSocketIP}
	results, err := plug.SystemInfo()
	if err != nil {
		log.Println("err:", err)
	}

	var systemInfo hs100

	err = json.Unmarshal([]byte(results), &systemInfo)

	if err != nil {
		panic(err)
	}

	return systemInfo.System.GetSysinfo.OnTime
}

func turnSocketOff(hs1xxSocketIP string) {
	plug := hs1xxplug.Hs1xxPlug{IPAddress: hs1xxSocketIP}
	err := plug.TurnOff()
	if err != nil {
		log.Println("err:", err)
	}
}

func turnSocketOn(hs1xxSocketIP string) {
	plug := hs1xxplug.Hs1xxPlug{IPAddress: hs1xxSocketIP}
	err := plug.TurnOn()
	if err != nil {
		log.Println("err:", err)
	}
}

func init() {
	// Metrics have to be registered to be exposed:
	prometheus.MustRegister(snmpTemp)
	prometheus.MustRegister(hs1xxRelayState)
}

func recordMetrics() {
	go func() {
		for {
			// reading environment variables
			switchIP, err := getEnv("SWITCH_IP")
			if err != nil {
				log.Fatal("error: ", err)
			}

			hs1xxSocketIP, err := getEnv("HS1XX_SOCKET_IP")
			if err != nil {
				log.Fatal("error: ", err)
			}

			snmpUsername, err := getEnv("SNMP_USERNAME")
			if err != nil {
				log.Fatal("error: ", err)
			}

			snmpPassword, err := getEnv("SNMP_PASSWORD")
			if err != nil {
				log.Fatal("error: ", err)
			}

			temperatureOID, err := getEnv("TEMPERATURE_OID")
			if err != nil {
				log.Fatal("error: ", err)
			}

			// temperature to turn on the fan
			tooHotString, err := getEnv("MAXIMUM_OFF_TEMPERATURE")
			if err != nil {
				log.Fatal("error: ", err)
			}

			tooHot, err := strconv.Atoi(tooHotString)
			if err != nil {
				log.Fatal("error: MAXIMUM_OFF_TEMPERATURE not an int", err)
			}

			// temperature to turn off the fan if already on
			coolEnoughString, err := getEnv("MINIMAL_ON_TEMPERATURE")
			if err != nil {
				log.Fatal("error: ", err)
			}

			coolEnough, err := strconv.Atoi(coolEnoughString)
			if err != nil {
				log.Fatal("error: MINIMAL_ON_TEMPERATURE not an int", err)
			}

			currentTemperature := getTemperature(switchIP, snmpUsername, snmpPassword, temperatureOID)

			snmpTemp.Set(float64(currentTemperature))

			smartSocketOnTime := getSocketOnUptime(hs1xxSocketIP)

			// keeping on or off logic and switching to on or off
			if currentTemperature >= tooHot {
				if smartSocketOnTime == 0 {
					log.Printf("Debug: temperature too hot at %d, will start cooling\n", currentTemperature)
					turnSocketOn(hs1xxSocketIP)
					hs1xxRelayState.Set(1)
				} else {
					log.Printf("Debug: temperature too hot at %d but on, will keep cooling\n", currentTemperature)
					hs1xxRelayState.Set(1)
				}
			} else if currentTemperature <= coolEnough {
				if smartSocketOnTime == 0 {
					log.Printf("Debug: temperature cool enough at %d and off, will keep off\n", currentTemperature)
					hs1xxRelayState.Set(0)
				} else {
					log.Printf("Debug: temperature cool enough at %d and on, will turn it off\n", currentTemperature)
					turnSocketOff(hs1xxSocketIP)
					hs1xxRelayState.Set(0)
				}
			} else {
				if smartSocketOnTime == 0 {
					log.Printf("Debug: temperature ok at %d, will keep off\n", currentTemperature)
					hs1xxRelayState.Set(0)
				} else {
					log.Printf("Debug: temperature ok at %d, will keep it running\n", currentTemperature)
					hs1xxRelayState.Set(1)
				}
			}
			// we can expose an environment variable for delay time
			// wait before checking temperature again
			time.Sleep(1 * time.Minute)
		}
	}()
}

func main() {
	recordMetrics()

	http.Handle("/metrics", promhttp.Handler())
	// selected the snmp exporter port
	http.ListenAndServe(":9116", nil)
}
