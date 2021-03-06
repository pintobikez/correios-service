package api

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	strut "github.com/pintobikez/brazilian-correios-service/api/structures"
	cnf "github.com/pintobikez/brazilian-correios-service/config/structures"
	hand "github.com/pintobikez/brazilian-correios-service/correiosapi"
	repo "github.com/pintobikez/brazilian-correios-service/repository"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
)

var (
	DeliveryTypes  = map[string]bool{"BDE": true, "BDI": true, "BDR": true}
	DeliveryFailed = map[string]string{"50": "Stolen", "51": "Stolen", "52": "Stolen", "80": "Lost"}
	DeliveryOk     = map[string]bool{"0": true, "1": true}
)

//Cronjob struct
type Cronjob struct {
	Repo repo.Definition
	Conf *cnf.CorreiosConfig
	Hand *hand.Handler
}

//New Initializes a new Cronjob struct
func New(r repo.Definition, c *cnf.CorreiosConfig) *Cronjob {
	return &Cronjob{Repo: r, Conf: c, Hand: &hand.Handler{Repo: r, Conf: c}}
}

//SetOutput sets the output file
func (c *Cronjob) SetOutput(file io.Writer) {
	log.SetOutput(file)
}

//CheckUpdatedReverses Handler to Check if reverse is completed
func (c *Cronjob) CheckUsedReverses(from int, offset int) {

	where := make([]*strut.SearchWhere, 0, 1)
	where = append(where, &strut.SearchWhere{Field: "status", Value: strut.StatusUsed, Operator: "="})

	search := &strut.Search{Where: where, From: from, Offset: offset}

	results, err := c.Repo.GetRequestBy(search)

	// something happened
	if err != nil {
		log.Printf("Error performing search %s", err.Error())
		return
	} else {

		s := len(results)
		els := make(map[string]*strut.Request)

		o := new(strut.Tracking)
		o.TrackingType = "LAST"
		o.Language = "EN"
		o.Objects = make([]string, 0, s)

		// get the tracking codes
		for _, e := range results {
			o.Callback = e.Callback
			o.Objects = append(o.Objects, e.TrackingCode)

			els[e.TrackingCode] = e
		}

		// check the tracking for all
		r, err := c.Hand.TrackObjects(o)

		if err != nil {
			log.Printf("Error performing tracking %s\n", err.Error())
			return
		}
		if r == nil {
			log.Println("Nothing to search for")
			return
		} else {
			// check if there are deliveries or failed delivies
			for _, e := range r.Items {

				if e.Error != "" && DeliveryTypes[e.Events[0].Type] {
					// delivered
					if _, ok := DeliveryOk[e.Events[0].StatusCode]; ok == true {
						el := els[e.Object]
						go c.Repo.UpdateRequestStatus(&strut.Request{RequestID: el.RequestID, Retries: 0}, strut.StatusDelivered, "Delivered")

						go doRequest(&strut.RequestResponse{el.RequestID, el.PostageCode, el.TrackingCode, strut.StatusDelivered, el.Callback})
					}

					// delivery failed
					if val, ok := DeliveryFailed[e.Events[0].StatusCode]; ok == true {
						el := els[e.Object]
						go c.Repo.UpdateRequestStatus(&strut.Request{RequestID: el.RequestID, Retries: 0}, strut.StatusFailedDelivery, val)

						go doRequest(&strut.RequestResponse{el.RequestID, el.PostageCode, el.TrackingCode, strut.StatusFailedDelivery, el.Callback})
					}
				}
			}
		}

		// if we still have more to process
		if s == offset {
			c.CheckUsedReverses((from + offset), offset)
		}
	}
}

//CheckUpdatedReverses Handler to Check if any updates have happened
func (c *Cronjob) CheckUpdatedReverses(requestType string) {
	resp := c.Hand.FollowReverseLogistic(requestType)

	// we have found something to process
	if resp != nil {
		for _, e := range resp {
			go doRequest(e)
		}
	}
}

//ReprocessRequestsWithError Handler to get all Requests with error and retry them again given a Max number of retries
func (c *Cronjob) ReprocessRequestsWithError() {

	where := make([]*strut.SearchWhere, 0, 2)
	where = append(where, &strut.SearchWhere{Field: "retries", Value: strconv.Itoa(int(c.Conf.MaxRetries)), Operator: "<="})
	where = append(where, &strut.SearchWhere{Field: "status", Value: strut.StatusError, Operator: "="})

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
				go doRequest(&strut.RequestResponse{e.RequestID, e.PostageCode, e.TrackingCode, e.Status, e.Callback})
			} else {
				go c.Hand.DoReverseLogistic(e)
			}
		}
	}
}

//doRequest Performs an Http request
func doRequest(e *strut.RequestResponse) {
	buffer := new(bytes.Buffer)
	_ = json.NewEncoder(buffer).Encode(e)

	// Create the POST request to the callback
	req, err := http.NewRequest("POST", e.Callback, buffer)
	if err != nil {
		// log error
		log.Println(err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Close = true

	// check if it is an https request
	re := regexp.MustCompile("^https://")
	useTlS := re.MatchString(e.Callback)

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: useTlS},
	}
	client := &http.Client{Transport: tr}
	res, err := client.Do(req)
	if err != nil {
		// log error
		log.Println(err.Error())
		return
	}
	defer res.Body.Close()
}
