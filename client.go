package teamcity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"
)

// Client to access a TeamCity API
type Client struct {
	HTTPClient *http.Client
	username   string
	password   string
	host       string
	debug      bool
}

func New(host, username, password string) *Client {
	return &Client{
		HTTPClient: http.DefaultClient,
		username:   username,
		password:   password,
		host:       host,
	}
}

func (c *Client) SetDebug(debug bool) {
	c.debug = debug
}

func (c *Client) QueueBuild(buildTypeID string, branchName string, properties map[string]string) (*Build, error) {
	jsonQuery := struct {
		BuildTypeID string `json:"buildTypeId,omitempty"`
		Properties  struct {
			Property []oneProperty `json:"property,omitempty"`
		} `json:"properties"`
		BranchName string `json:"branchName,omitempty"`
		Personal string `json:"personal,omitempty"`
	}{}
	jsonQuery.BuildTypeID = buildTypeID
	jsonQuery.Personal = "true"
	if branchName != "" {
		//jsonQuery.BranchName = fmt.Sprintf("refs/heads/%s", branchName)
		jsonQuery.BranchName = branchName
	}
	for k, v := range properties {
		jsonQuery.Properties.Property = append(jsonQuery.Properties.Property, oneProperty{k, v})
	}

	build := &Build{}

	retries := 8
	err := withRetry(retries, func() error {
		return c.doRequest("POST", "/httpAuth/app/rest/buildQueue", jsonQuery, &build)
	})
	if err != nil {
		return nil, err
	}

	build.convertInputs()

	return build, nil
}

func (c *Client) SearchBuild(locator string) ([]*Build, error) {
	path := fmt.Sprintf("/httpAuth/app/rest/builds/?locator=%s&fields=count,build(*,tags(tag),triggered(*),properties(property),problemOccurrences(*,problemOccurrence(*)),testOccurrences(*,testOccurrence(*)),changes(*,change(*)))", locator)

	respStruct := struct {
		Count int
		Build []*Build
	}{}
	retries := 8
	err := withRetry(retries, func() error {
		return c.doRequest("GET", path, nil, &respStruct)
	})
	if err != nil {
		return nil, err
	}

	for _, build := range respStruct.Build {
		build.convertInputs()
	}

	return respStruct.Build, nil
}

func (c *Client) GetQueuedBuilds(locator string) ([]*Build, error) {
	path := fmt.Sprintf(
		"/httpAuth/app/rest/buildQueue?locator=%s&fields="+
		"count,"+
		"build(" +
			"*," +
			"tags(tag)," +
			"triggered(*)," +
			"properties(property)," +
			"problemOccurrences(*,problemOccurrence(*)),"+
			"testOccurrences(*,testOccurrence(*)),"+
			"changes(*,change(*))" +
		")",
		locator,
	)
	respStruct := struct {
		Count int
		Build []*Build
	}{}
	retries := 8
	err := withRetry(retries, func() error {
		return c.doRequest("GET", path, nil, &respStruct)
	})
	if err != nil {
		return nil, err
	}

	for _, build := range respStruct.Build {
		build.convertInputs()
	}

	return respStruct.Build, nil
}

func (c *Client) GetBuild(buildID string) (*Build, error) {
	path := fmt.Sprintf("/httpAuth/app/rest/builds/id:%s?fields=*,tags(tag),triggered(*),properties(property),problemOccurrences(*,problemOccurrence(*)),testOccurrences(*,testOccurrence(*)),changes(*,change(*))", buildID)
	var build *Build

	retries := 8
	err := withRetry(retries, func() error {
		return c.doRequest("GET", path, nil, &build)
	})

	if err != nil {
		return nil, err
	}

	if build == nil {
		return nil, errors.New("build not found")
	}

	return build, nil
}

func (c *Client) GetBuildID(buildTypeID, branchName, buildNumber string) (string, error) {
	type builds struct {
		Count    int
		Href     string
		NextHref string
		Build    []Build
	}

	path := fmt.Sprintf("/httpAuth/app/rest/buildTypes/id:%s/builds?locator=branch:%s,number:%s,count:1", buildTypeID, branchName, buildNumber)

	var build *builds
	retries := 8
	err := withRetry(retries, func() error {
		return c.doRequest("GET", path, nil, &build)
	})
	if err != nil {
		return "ID not found", err
	}

	if build == nil {
		return "ID not found", errors.New("build not found")
	}

	return fmt.Sprintf("%d", build.Build[0].ID), nil
}

func (c *Client) GetBuildProperties(buildID string) (map[string]string, error) {
	path := fmt.Sprintf("/httpAuth/app/rest/builds/id:%s/resulting-properties", buildID)

	var response struct {
		Property []oneProperty `json:"property,omitempty"`
	}

	retries := 8
	err := withRetry(retries, func() error {
		return c.doRequest("GET", path, nil, &response)
	})
	if err != nil {
		return nil, err
	}

	m := make(map[string]string)
	for _, prop := range response.Property {
		m[prop.Name] = prop.Value
	}
	return m, nil
}

func (c *Client) GetChanges(path string) ([]Change, error) {
	var changes struct {
		Change []Change
	}

	path += ",count:99999"
	err := c.doRequest("GET", path, nil, &changes)
	if err != nil {
		return nil, err
	}

	if changes.Change == nil {
		return nil, errors.New("changes not found")
	}

	return changes.Change, nil
}

func (c *Client) GetProblems(path string, count int64) ([]ProblemOccurrence, error) {
	var problems struct {
		Count             int64
		Default           bool
		ProblemOccurrence []ProblemOccurrence
	}

	path += fmt.Sprintf(",count:%v&fields=*,problemOccurrence(*,details)", count)
	err := c.doRequest("GET", path, nil, &problems)
	if err != nil {
		return nil, err
	}

	if problems.ProblemOccurrence == nil {
		return nil, errors.New("problemOccurrence list not found")
	}

	return problems.ProblemOccurrence, nil
}

func (c *Client) GetTests(path string, count int64, failingOnly bool, ignoreMuted bool) ([]TestOccurrence, error) {
	var tests struct {
		Count          int64
		HREF           string
		TestOccurrence []TestOccurrence
	}

	if ignoreMuted {
		path += ",currentlyMuted:false"
	}
	if failingOnly {
		path += ",status:FAILURE"
	}
	path += fmt.Sprintf(",count:%v", count)
	err := c.doRequest("GET", path, nil, &tests)
	if err != nil {
		return nil, err
	}

	return tests.TestOccurrence, nil
}

func (c *Client) CancelBuild(buildID int64, comment string) error {
	body := map[string]interface{}{
		"buildCancelRequest": map[string]interface{}{
			"comment":       comment,
			"readIntoQueue": true,
		},
	}
	return c.doRequest("POST", fmt.Sprintf("/httpAuth/app/rest/id:%d", buildID), body, nil)
}

func (c *Client) GetBuildLog(buildID string) (string, error) {
	cnt, err := c.doNotJSONRequest("GET", fmt.Sprintf("/httpAuth/downloadBuildLog.html?buildId=%s", buildID), nil)
	buf := bytes.NewBuffer(cnt)
	return buf.String(), err
}

func (c *Client) doRequest(method string, path string, data interface{}, v interface{}) error {
	jsonCnt, err := c.doNotJSONRequest(method, path, data)
	if err != nil {
		return err
	}

	ioutil.WriteFile(fmt.Sprintf("/tmp/mama-%s.json", time.Now().Format("15h04m05.000")), jsonCnt, 0644)

	if v != nil {
		err = json.Unmarshal(jsonCnt, &v)
		if err != nil {
			return fmt.Errorf("json unmarshal: %s (%q)", err, truncate(string(jsonCnt), 1000))
		}
	}

	return nil
}

func (c *Client) addProtocol(path string) string {
	//Perform some validation on host. Allow them to specify http vs https
	//if desired and remove trailing slash if present
	host := c.host
	if strings.HasSuffix(host, "/") {
		host = strings.TrimSuffix(host, "/")
	}
	prefix := "https://"
	if strings.Contains(strings.ToLower(host), "http") {
		prefix = ""
	}
	return fmt.Sprintf("%s%s%s", prefix, host, path)
}

func (c *Client) doNotJSONRequest(method string, path string, data interface{}) ([]byte, error) {
	authURL := c.addProtocol(path)

	if c.debug {
		fmt.Printf("Sending request to %s\n", authURL)
	}

	var body io.Reader
	if data != nil {
		jsonReq, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("marshaling data: %s", err)
		}

		body = bytes.NewBuffer(jsonReq)
	}

	req, _ := http.NewRequest(method, authURL, body)
	req.SetBasicAuth(c.username, c.password)
	req.Header.Add("Accept", "application/json")

	if body != nil {
		req.Header.Add("Content-Type", "application/json")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return ioutil.ReadAll(resp.Body)
}

func truncate(s string, l int) string {
	if len(s) > l {
		return s[:l]
	}
	return s
}

func withRetry(retries int, f func() error) (err error) {
	for i := 0; i < retries; i++ {
		err = f()
		if err != nil {
			log.Printf("Retry: %v / %v, error: %v\n", i, retries, err)
		} else {
			return
		}
	}
	return
}
