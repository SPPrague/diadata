package alephiumhelper

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"strconv"
	"time"

	"github.com/diadata-org/diadata/pkg/dia"
	"github.com/sirupsen/logrus"
)

const (
	BackendURL              = "https://backend.mainnet.alephium.org"
	NodeURL                 = "https://node.mainnet.alephium.org"
	AYINPairContractAddress = "vyrkJHG49TXss6pGAz2dVxq5o7mBXNNXAV18nAeqVT1R"
)

const (
	SymbolMethod = iota
	NameMethod
	DecimalsMethod
	TokenPairMethod = 7
)

const (
	SwapEventIndex = 2
)

const (
	DefaultRefreshDelay              = 400 // millisec
	DefaultSleepBetweenContractCalls = 300 // millisec
	DefaultEventsLimit               = 100
	DefaultSwapContractsLimit        = 100
)

// ALPHNativeToken: native alephium token - it has no related contract
// details -> https://github.com/alephium/token-list/blob/master/tokens/mainnet.json#L4-L11
var ALPHNativeToken = dia.Asset{
	Address:  "tgx7VNFoP9DJiFMFgXXtafQZkUvyEdDHT9ryamHJYrjq",
	Symbol:   "ALPH",
	Decimals: 18,
	Name:     "Alephium",
}

// AlephiumClient: interaction with alephium REST API with urls from @BackendURL, @NodeURL contants
type AlephiumClient struct {
	Debug             bool
	HTTPClient        *http.Client
	logger            *logrus.Entry
	sleepBetweenCalls time.Duration
}

// NewAlephiumClient returns AlephiumClient
func NewAlephiumClient(logger *logrus.Entry, sleepBetweenCalls time.Duration, debug bool) *AlephiumClient {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			MaxVersion: 0,
		},
	}
	httpClient := &http.Client{
		Transport: tr,
		Timeout:   10 * time.Second,
	}

	result := &AlephiumClient{
		HTTPClient:        httpClient,
		Debug:             debug,
		logger:            logger,
		sleepBetweenCalls: sleepBetweenCalls,
	}

	return result
}

func (c *AlephiumClient) callAPI(request *http.Request, target interface{}) error {
	if c.Debug {
		dump, err := httputil.DumpRequestOut(request, true)
		if err != nil {
			return err
		}
		log.Printf("DumpRequestOut: \n%s\n", string(dump))
	}

	resp, err := c.HTTPClient.Do(request)
	if err != nil {
		return err
	}

	if c.Debug && resp != nil {
		dump, err := httputil.DumpResponse(resp, true)
		if err != nil {
			return err
		}
		c.logger.Printf("\n%s\n", string(dump))
	}
	data, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		err = errors.New("not 200 http response code from api")
		c.logger.
			WithError(err).
			WithField("resp.StatusCode", resp.StatusCode).
			WithField("body", string(data)).
			WithField("url", request.URL).
			Error("failed to call api")
		return err
	}

	err = json.Unmarshal(data, &target)
	if err != nil {
		return err
	}

	c.waiting()

	return resp.Body.Close()
}

// GetSwapPairsContractAddresses returns swap contract addresses for alephium
func (c *AlephiumClient) GetSwapPairsContractAddresses(swapContractsLimit int) (SubContractResponse, error) {
	var contractResponsePage1, contractResponsePage2 SubContractResponse

	// Page 1
	url := fmt.Sprintf("%s/contracts/%s/sub-contracts?limit=%d&page=1", BackendURL, AYINPairContractAddress, swapContractsLimit)
	request, _ := http.NewRequest("GET", url, http.NoBody)
	err := c.callAPI(request, &contractResponsePage1)
	if err != nil {
		return contractResponsePage1, err
	}

	// Page 2
	url = fmt.Sprintf("%s/contracts/%s/sub-contracts?limit=%d&page=2", BackendURL, AYINPairContractAddress, swapContractsLimit)
	request, _ = http.NewRequest("GET", url, http.NoBody)
	err = c.callAPI(request, &contractResponsePage2)
	if err != nil {
		return contractResponsePage1, err
	}

	for _, contract := range contractResponsePage2.SubContracts {
		contractResponsePage1.SubContracts = append(contractResponsePage1.SubContracts, contract)
	}
	return contractResponsePage1, nil
}

// GetTokenPairAddresses returns token address pair for swap contract address
func (c *AlephiumClient) GetTokenPairAddresses(contractAddress string) ([]string, error) {
	group, err := groupOfAddress(contractAddress)
	if err != nil {
		return nil, err
	}
	inputData := CallContractRequest{
		Group:       int(group),
		Address:     contractAddress,
		MethodIndex: TokenPairMethod,
	}
	logger := c.logger.
		WithField("function", "GetTokenPairAddresses").
		WithField("contractAddress", contractAddress)

	jsonData, err := json.Marshal(inputData)

	if err != nil {
		logger.Fatalf("failed to marshal input data: %v", err)
		return nil, err
	}
	url := fmt.Sprintf("%s/contracts/call-contract", NodeURL)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		logger.Fatalf("failed to create request: %v", err)
		return nil, err
	}
	var response CallContractResult
	err = c.callAPI(req, &response)

	if err != nil {
		logger.WithError(err).Error("failed to callApi")
		return nil, err
	}
	if response.Error != nil {
		err = errors.New(*response.Error)
		logger.
			WithError(err).
			WithField("jsonData", string(jsonData)).
			WithField("contractAddress", contractAddress).
			Error("failed to get token pair")
		return nil, err
	}

	address1, err := AddressFromTokenId(response.Returns[0].Value)
	if err != nil {
		logger.WithError(err).Error("failed to calculate address1")
		return nil, err
	}
	address2, err := AddressFromTokenId(response.Returns[1].Value)
	if err != nil {
		logger.WithError(err).Error("failed to calculate address2")
		return nil, err
	}

	output := []string{address1, address2}
	return output, nil
}

// GetTokenInfoForContractDecoded returns alephium token metainfo, decoded to dia.Asset struct
func (c *AlephiumClient) GetTokenInfoForContractDecoded(contractAddress, blockchain string) (*dia.Asset, error) {
	inputData := make([]CallContractRequest, 0)
	logger := c.logger.WithField("function", "GetTokenInfoForContract")

	if contractAddress == ALPHNativeToken.Address {
		return &ALPHNativeToken, nil
	}
	for i := 0; i < 3; i++ {
		group, err := groupOfAddress(contractAddress)
		if err != nil {
			return nil, err
		}
		row := CallContractRequest{
			Group:       int(group),
			Address:     contractAddress,
			MethodIndex: i,
		}
		inputData = append(inputData, row)
	}

	calls := Calls{Calls: inputData}
	jsonData, err := json.Marshal(calls)

	if err != nil {
		logger.Fatalf("failed to marshal input data: %v", err)
		return nil, err
	}
	url := fmt.Sprintf("%s/contracts/multicall-contract", NodeURL)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))

	if err != nil {
		logger.Fatalf("failed to create request: %v", err)
		return nil, err
	}

	var response MulticallContractResponse
	err = c.callAPI(req, &response)

	if err != nil {
		logger.WithError(err).Error("failed to callApi")
		return nil, err
	}
	output := OutputResult{
		Address: contractAddress,
		Results: []OutputField{},
	}
	for _, row := range response.Results {
		if row.Error != nil {
			err = errors.New(*row.Error)
			logger.
				WithError(err).
				WithField("jsonData", string(jsonData)).
				WithField("contractAddress", contractAddress).
				Error("failed to get token info")
			return nil, err
		}
		result := OutputField{
			ResponseResult: row.Type,
			Field:          row.Returns[0],
		}
		output.Results = append(output.Results, result)
	}
	asset, err := c.decodeMulticallRequestToAssets(contractAddress, blockchain, &output)

	return &asset, err
}

// GetCurrentHeight returns the current height (block number) in Alephium network
func (c *AlephiumClient) GetCurrentHeight() (int, error) {
	logger := c.logger.WithField("function", "GetLatestBlockHash")

	url := fmt.Sprintf("%s/blockflow/chain-info?fromGroup=0&toGroup=0", NodeURL)
	request, _ := http.NewRequest("GET", url, http.NoBody)

	var response ChainInfoResponse
	err := c.callAPI(request, &response)

	if err != nil {
		logger.WithError(err).Error("failed to callApi")
		return 0, err
	}

	return response.CurrentHeight, nil
}

// GetBlockHashes returns all block hashes at a given height from REST API
func (c *AlephiumClient) GetBlockHashes(height int) ([]string, error) {
	logger := c.logger.WithField("function", "GetBlockHashes")

	url := fmt.Sprintf("%s/blockflow/hashes?fromGroup=0&toGroup=0&height=%d", NodeURL, height)
	request, _ := http.NewRequest("GET", url, http.NoBody)

	var response BlockHashesResponse
	err := c.callAPI(request, &response)

	if err != nil {
		logger.WithError(err).Error("failed to callApi")
		return nil, err
	}

	return response.Headers, nil
}

// GetContractEvents returns events included in a specific block from REST API
func (c *AlephiumClient) GetBlockEvents(blockHash string) ([]ContractEvent, error) {
	logger := c.logger.WithField("function", "GetEvents")

	url := fmt.Sprintf("%s/events/block-hash/%s?group=0", NodeURL, blockHash)
	request, _ := http.NewRequest("GET", url, http.NoBody)

	var response BlockEventsResponse
	err := c.callAPI(request, &response)

	if err != nil {
		logger.WithError(err).Error("failed to callApi")
		return nil, err
	}

	return response.Events, nil
}

// GetSwapContractEvents returns swap event transaction details by transaction hash
func (c *AlephiumClient) GetTransactionDetails(txnHash string) (TransactionDetailsResponse, error) {
	logger := c.logger.WithField("function", "GetTransactionDetails")

	// 'https://backend.mainnet.alephium.org/transactions/b9744b60b94a342c488dbf827747e5ac8ff8adabce48a72167f0ce3dfbe8291a
	url := fmt.Sprintf("%s/transactions/%s", BackendURL, txnHash)
	request, _ := http.NewRequest("GET", url, http.NoBody)

	var transactionDetailsResponse TransactionDetailsResponse
	err := c.callAPI(request, &transactionDetailsResponse)

	if err != nil {
		logger.WithError(err).Error("failed to callApi")
		return transactionDetailsResponse, err
	}
	return transactionDetailsResponse, nil
}

func (s *AlephiumClient) FilterEvents(allEvents []ContractEvent, filter int) []ContractEvent {
	events := make([]ContractEvent, 0, len(allEvents))
	for _, event := range allEvents {
		if event.EventIndex == filter {
			events = append(events, event)
		}
	}
	return events
}

func (c *AlephiumClient) GetContractState(address string) (ContractStateResponse, error) {
	logger := c.logger.WithField("function", "GetContractState")
	// https://node.mainnet.alephium.org/contracts/22po9GJCMoLcYgXL3Znv2cSXcMnKmfm36MrBdqB4rSoKV/state
	url := fmt.Sprintf("%s/contracts/%s/state", NodeURL, address)
	request, _ := http.NewRequest("GET", url, http.NoBody)

	var contractStateResponse ContractStateResponse
	err := c.callAPI(request, &contractStateResponse)
	if err != nil {
		logger.WithError(err).Error("failed to callApi")
		return contractStateResponse, err
	}
	return contractStateResponse, nil
}

func (s *AlephiumClient) decodeMulticallRequestToAssets(contractAddress, blockchain string, resp *OutputResult) (dia.Asset, error) {
	asset := dia.Asset{}

	symbol, err := DecodeHex(resp.Results[SymbolMethod].Value)
	if err != nil {
		s.logger.
			WithField("row", resp).
			WithError(err).
			Error("failed to decode symbol")
		return asset, err
	}
	asset.Symbol = symbol

	name, err := DecodeHex(resp.Results[NameMethod].Value)
	if err != nil {
		s.logger.
			WithField("row", resp).
			WithError(err).
			Error("failed to decode name")
		return asset, err
	}
	asset.Name = name

	decimals, err := strconv.ParseUint(resp.Results[DecimalsMethod].Value, 10, 32)
	if err != nil {
		s.logger.
			WithField("row", resp).
			WithError(err).
			Error("failed to decode decimals")
		return asset, err
	}
	asset.Decimals = uint8(decimals)
	asset.Address = contractAddress
	asset.Blockchain = blockchain

	return asset, nil
}

func (c *AlephiumClient) waiting() {
	time.Sleep(c.sleepBetweenCalls)
}
