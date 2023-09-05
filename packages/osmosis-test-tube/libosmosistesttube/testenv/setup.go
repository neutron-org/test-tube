package testenv

import (
	"encoding/json"
	"fmt"
	"github.com/cosmos/cosmos-sdk/codec"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	ccvconsumertypes "github.com/cosmos/interchain-security/x/ccv/consumer/types"
	"github.com/neutron-org/neutron/testutil/consumer"
	"strings"
	"time"

	// helpers

	abci "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/libs/log"
	tmprototypes "github.com/tendermint/tendermint/proto/tendermint/types"
	tmtypes "github.com/tendermint/tendermint/types"
	dbm "github.com/tendermint/tm-db"

	// cosmos-sdk

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/cosmos/cosmos-sdk/simapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	// wasmd
	"github.com/CosmWasm/wasmd/x/wasm"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"

	// neutron
	"github.com/neutron-org/neutron/app"
)

type TestEnv struct {
	App                *app.App
	Ctx                sdk.Context
	ParamTypesRegistry ParamTypeRegistry
	ValPrivs           []*secp256k1.PrivKey
	NodeHome           string
	FaucetAccount      *secp256k1.PrivKey
}

// DebugAppOptions is a stub implementing AppOptions
type DebugAppOptions struct{}

// Get implements AppOptions
func (ao DebugAppOptions) Get(o string) interface{} {
	if o == server.FlagTrace {
		return true
	}
	return nil
}

func SetupOsmosisApp(nodeHome string, faucet sdk.AccAddress) *app.App {
	db := dbm.NewMemDB()

	encCfg := app.MakeEncodingConfig()
	cfg := app.GetDefaultConfig()
	cfg.Seal()

	appInstance := app.New(
		log.NewNopLogger(),
		db,
		nil,
		true,
		map[int64]bool{},
		app.DefaultNodeHome,
		0,
		encCfg,
		app.GetEnabledProposals(),
		simapp.EmptyAppOptions{},
		nil,
	)

	genesisState := app.NewDefaultGenesisState(encCfg.Marshaler)

	// Set up Wasm genesis state
	wasmGen := wasm.GenesisState{
		Params: wasmtypes.Params{
			// Allow store code without gov
			CodeUploadAccess:             wasmtypes.AllowEverybody,
			InstantiateDefaultPermission: wasmtypes.AccessTypeEverybody,
		},
	}
	genesisState[wasm.ModuleName] = encCfg.Marshaler.MustMarshalJSON(&wasmGen)

	// setup consumer genesis state
	consumerGenesis := createConsumerGenesis()
	genesisState[ccvconsumertypes.ModuleName] = encCfg.Marshaler.MustMarshalJSON(consumerGenesis)

	//Add Faucet account
	AddGenesisAccount(encCfg.Marshaler, genesisState, faucet)

	stateBytes, err := json.MarshalIndent(genesisState, "", " ")

	requireNoErr(err)

	concensusParams := simapp.DefaultConsensusParams
	concensusParams.Block = &abci.BlockParams{
		MaxBytes: 22020096,
		MaxGas:   -1,
	}

	// replace sdk.DefaultDenom with "uosmo", a bit of a hack, needs improvement
	stateBytes = []byte(strings.Replace(string(stateBytes), "\"stake\"", "\"uosmo\"", -1))

	appInstance.InitChain(
		abci.RequestInitChain{
			Validators:      []abci.ValidatorUpdate{},
			ConsensusParams: concensusParams,
			AppStateBytes:   stateBytes,
		},
	)

	return appInstance
}

func AddGenesisAccount(cdc codec.Codec, genesisState app.GenesisState, addr sdk.AccAddress) {
	coins, err := sdk.ParseCoinsNormalized("2000000000000000000untrn")
	requireNoErr(err)
	balances := banktypes.Balance{Address: addr.String(), Coins: coins.Sort()}
	bankGenState := banktypes.GetGenesisStateFromAppState(cdc, genesisState)
	bankGenState.Balances = append(bankGenState.Balances, balances)
	bankGenState.Balances = banktypes.SanitizeGenesisBalances(bankGenState.Balances)
	bankGenState.Supply = bankGenState.Supply.Add(balances.Coins...)

	authGenState := authtypes.GetGenesisStateFromAppState(cdc, genesisState)

	accs, err := authtypes.UnpackAccounts(authGenState.Accounts)
	requireNoErr(err)
	baseAccount := authtypes.NewBaseAccount(addr, nil, 0, 0)
	accs = append(accs, baseAccount)
	accs = authtypes.SanitizeGenesisAccounts(accs)

	genAccs, err := authtypes.PackAccounts(accs)
	requireNoErr(err)

	authGenState.Accounts = genAccs

	genesisState[authtypes.ModuleName] = cdc.MustMarshalJSON(&authGenState)
	genesisState[banktypes.ModuleName] = cdc.MustMarshalJSON(bankGenState)
}

func createConsumerGenesis() *ccvconsumertypes.GenesisState {
	genesisState := consumer.CreateMinimalConsumerTestGenesis()
	valPriv := secp256k1.GenPrivKey()
	valPub := valPriv.PubKey()
	tmProtoPublicKey, err := cryptocodec.ToTmProtoPublicKey(valPub)
	requireNoErr(err)
	initialValset := []abci.ValidatorUpdate{{PubKey: tmProtoPublicKey, Power: 100}}
	vals, err := tmtypes.PB2TM.ValidatorUpdates(initialValset)
	requireNoErr(err)

	genesisState.InitialValSet = initialValset
	genesisState.ProviderConsensusState.NextValidatorsHash = tmtypes.NewValidatorSet(vals).Hash()
	return genesisState
}

func (env *TestEnv) BeginNewBlock(executeNextEpoch bool, timeIncreaseSeconds uint64) {
	var valAddr []byte

	//env.App.ConsumerKeeper.GetAllCCValidator()

	validators := env.App.ConsumerKeeper.GetAllCCValidator(env.Ctx)
	if len(validators) >= 1 {
		pk, err := validators[0].ConsPubKey()
		requireNoErr(err)
		valAddrFancy := sdk.ConsAddress(pk.Address())
		valAddr = valAddrFancy.Bytes()
	} else {
		panic("ccv consumer section is not configured")
	}

	env.beginNewBlockWithProposer(executeNextEpoch, valAddr, timeIncreaseSeconds)
}

func (env *TestEnv) GetValidatorAddresses() []string {
	validators := env.App.ConsumerKeeper.GetAllCCValidator(env.Ctx)
	var addresses []string
	for _, validator := range validators {
		addresses = append(addresses, sdk.ValAddress(validator.GetAddress()).String())
	}

	return addresses
}

// beginNewBlockWithProposer begins a new block with a proposer.
func (env *TestEnv) beginNewBlockWithProposer(executeNextEpoch bool, proposer sdk.ValAddress, timeIncreaseSeconds uint64) {
	validator, found := env.App.ConsumerKeeper.GetCCValidator(env.Ctx, proposer)

	if !found {
		panic("validator not found")
	}

	//valConsAddr, err :=
	//requireNoErr(err)

	valAddr := validator.GetAddress()

	//epochIdentifier := env.App.SuperfluidKeeper.GetEpochIdentifier(env.Ctx)
	//epoch := env.App.EpochsKeeper.GetEpochInfo(env.Ctx, epochIdentifier)
	newBlockTime := env.Ctx.BlockTime().Add(time.Duration(timeIncreaseSeconds) * time.Second)
	//if executeNextEpoch {
	//	newBlockTime = env.Ctx.BlockTime().Add(epoch.Duration).Add(time.Second)
	//}

	header := tmprototypes.Header{ChainID: "osmosis-1", Height: env.Ctx.BlockHeight() + 1, Time: newBlockTime}
	newCtx := env.Ctx.WithBlockTime(newBlockTime).WithBlockHeight(env.Ctx.BlockHeight() + 1)
	env.Ctx = newCtx
	lastCommitInfo := abci.LastCommitInfo{
		Votes: []abci.VoteInfo{{
			Validator:       abci.Validator{Address: valAddr, Power: 1000},
			SignedLastBlock: true,
		}},
	}
	reqBeginBlock := abci.RequestBeginBlock{Header: header, LastCommitInfo: lastCommitInfo}

	env.App.BeginBlock(reqBeginBlock)
	env.Ctx = env.App.NewContext(false, reqBeginBlock.Header)
}

//func (env *TestEnv) setupValidator(bondStatus stakingtypes.BondStatus) (*secp256k1.PrivKey, sdk.ValAddress) {
//	valPriv := secp256k1.GenPrivKey()
//	valPub := valPriv.PubKey()
//	valAddr := sdk.ValAddress(valPub.Address())
//	bondDenom := env.App.StakingKeeper.GetParams(env.Ctx).BondDenom
//	selfBond := sdk.NewCoins(sdk.Coin{Amount: sdk.NewInt(100), Denom: bondDenom})
//
//	err := simapp.FundAccount(env.App.BankKeeper, env.Ctx, sdk.AccAddress(valPub.Address()), selfBond)
//	requireNoErr(err)
//
//	stakingHandler := staking.NewHandler(*env.App.StakingKeeper)
//	stakingCoin := sdk.NewCoin(bondDenom, selfBond[0].Amount)
//	ZeroCommission := stakingtypes.NewCommissionRates(sdk.ZeroDec(), sdk.ZeroDec(), sdk.ZeroDec())
//	msg, err := stakingtypes.NewMsgCreateValidator(valAddr, valPub, stakingCoin, stakingtypes.Description{}, ZeroCommission, sdk.OneInt())
//	requireNoErr(err)
//	res, err := stakingHandler(env.Ctx, msg)
//	requireNoErr(err)
//	requireNoNil("staking handler", res)
//
//	env.App.BankKeeper.SendCoinsFromModuleToModule(env.Ctx, stakingtypes.NotBondedPoolName, stakingtypes.BondedPoolName, sdk.NewCoins(stakingCoin))
//
//	val, found := env.App.StakingKeeper.GetValidator(env.Ctx, valAddr)
//	requierTrue("validator found", found)
//
//	val = val.UpdateStatus(bondStatus)
//	env.App.StakingKeeper.SetValidator(env.Ctx, val)
//
//	consAddr, err := val.GetConsAddr()
//	requireNoErr(err)
//
//	signingInfo := slashingtypes.NewValidatorSigningInfo(
//		consAddr,
//		env.Ctx.BlockHeight(),
//		0,
//		time.Unix(0, 0),
//		false,
//		0,
//	)
//	env.App.SlashingKeeper.SetValidatorSigningInfo(env.Ctx, consAddr, signingInfo)
//
//	return valPriv, valAddr
//}

func (env *TestEnv) SetupParamTypes() {
	//pReg := env.ParamTypesRegistry

	//pReg.RegisterParamSet(&lockuptypes.Params{})
	//pReg.RegisterParamSet(&incentivetypes.Params{})
	//pReg.RegisterParamSet(&minttypes.Params{})
	//pReg.RegisterParamSet(&twaptypes.Params{})
	//pReg.RegisterParamSet(&gammtypes.Params{})
	//pReg.RegisterParamSet(&ibcratelimittypes.Params{})
	//pReg.RegisterParamSet(&tokenfactorytypes.Params{})
	//pReg.RegisterParamSet(&superfluidtypes.Params{})
	//pReg.RegisterParamSet(&poolincentivetypes.Params{})
	//pReg.RegisterParamSet(&protorevtypes.Params{})
	//pReg.RegisterParamSet(&poolmanagertypes.Params{})
	//pReg.RegisterParamSet(&concentrateliquiditytypes.Params{})
}

func requireNoErr(err error) {
	if err != nil {
		panic(err)
	}
}

func requireNoNil(name string, nilable any) {
	if nilable == nil {
		panic(fmt.Sprintf("%s must not be nil", name))
	}
}

func requierTrue(name string, b bool) {
	if !b {
		panic(fmt.Sprintf("%s must be true", name))
	}
}
