package acalaswap

import (
	"github.com/centrifuge/go-substrate-rpc-client/v4/types"
)

type ForeignAssetID types.U16
type StableAssetPoolID types.U32
type AssetIds struct {
	Erc20          EvmAddress
	StableAssetID  StableAssetPoolID
	ForeignAssetID ForeignAssetID
	NativeAssetID  CurrencyID
}

type AcalaAssetMetadata struct {
	Name           []types.U8
	Symbol         []types.U8
	Decimals       types.U8
	MinimalBalance Balance
}
