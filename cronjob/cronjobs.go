package api

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	strut "github.com/pintobikez/correios-service/api/structures"
	cnf "github.com/pintobikez/correios-service/config/structures"
	hand "github.com/pintobikez/correios-service/correiosapi"
	repo "github.com/pintobikez/correios-service/repository"
	"log"
	"net/http"
	"regexp"
	"strconv"
)

type Cronjob struct {
	Repo repo.RepositoryDefinition
	Conf *cnf.CorreiosConfig
	Hand *hand.Handler
}

func New(r repo.RepositoryDefinition, c *cnf.CorreiosConfig) *Cronjob {
	return &Cronjob{Repo: r, Conf: c, Hand: &hand.Handler{Repo: r, Conf: c}}
}

// Handler to Check if any updates have happened
func (c *Cronjob) CheckUpdatedReverses(requestType string) {
	resp := c.Hand.FollowReverseLogistic(requestType)

	// we have found something to process
	if resp != nil {
		for _, e := range resp {
			go doRequest(e)
		}
	}
}

// Handler to get all Requests with error and retry them again given a Max number of retries
func (c *Cronjob) ReprocessRequestsWithError() {

	where := make([]*strut.SearchWhere, 0, 2)
	where = append(where, &strut.SearchWhere{Field: "retries", Value: strconv.Itoa(int(c.Conf.MaxRetries)), Operator: "<="})
	where = append(where, &strut.SearchWhere{Field: "status", Value: strut.STATUS_ERROR, Operator: "="})

	search := &strut.Search{Where: where}

	results, err := c.Repo.GetRequestBy(search)

	// something happened
	if err != nil {
		log.Printf("Error performing search %s", err.Error())
	} else {
		// retry all of the requests
		for _, e := range results {
			// If we reached MAX retries, do callback to requirer
			if e.Retries == c.Conf.MaxRetries {
				go doRequest(&strut.RequestResponse{e.RequestId, e.PostageCode, e.TrackingCode, e.Status, e.Callback})
			} else {
				go c.Hand.DoReverseLogistic(e)
			}
		}
	}
}

// Performs an Http request
func doRequest(e *strut.RequestResponse) {
	buffer := new(bytes.Buffer)
	_ = json.NewEncoder(buffer).Encode(e)

	// Create the POST request to the callback
	req, err := http.NewRequest("POST", e.Callback, buffer)
	if err != nil {
		// log error
		fmt.Println(err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Close = true

	// check if it is an https request
	re := regexp.MustCompile("^https://")
	useTls := re.MatchString(e.Callback)

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: useTls},
	}
	client := &http.Client{Transport: tr}
	res, err := client.Do(req)
	if err != nil {
		// log error
		fmt.Println(err.Error())
		return
	}
	defer res.Body.Close()
}