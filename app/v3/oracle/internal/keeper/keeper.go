package keeper

import (
	"github.com/tidwall/gjson"

	"github.com/irisnet/irishub/app/v3/oracle/internal/types"
	"github.com/irisnet/irishub/codec"
	sdk "github.com/irisnet/irishub/types"
)

// Keeper
type Keeper struct {
	storeKey sdk.StoreKey
	cdc      *codec.Codec

	gk types.GuardianKeeper
	sk types.ServiceKeeper
	// codespace
	codespace sdk.CodespaceType
}

// NewKeeper
func NewKeeper(cdc *codec.Codec, key sdk.StoreKey,
	codespace sdk.CodespaceType,
	gk types.GuardianKeeper,
	sk types.ServiceKeeper) Keeper {
	keeper := Keeper{
		storeKey:  key,
		cdc:       cdc,
		gk:        gk,
		sk:        sk,
		codespace: codespace,
	}
	_ = sk.RegisterResponseCallback(types.ModuleName, keeper.HandlerResponse)
	return keeper
}

//CreateFeed create a stopped feed
func (k Keeper) CreateFeed(ctx sdk.Context, msg types.MsgCreateFeed) sdk.Error {
	_, existed := k.gk.GetProfiler(ctx, msg.Creator)
	if !existed {
		return types.ErrNotProfiler(types.DefaultCodespace, msg.Creator)
	}

	if _, found := k.GetFeed(ctx, msg.FeedName); found {
		return types.ErrExistedFeedName(types.DefaultCodespace, msg.FeedName)
	}

	requestContextID, err := k.sk.CreateRequestContext(ctx,
		msg.ServiceName,
		msg.Providers,
		msg.Creator,
		msg.Input,
		msg.ServiceFeeCap,
		msg.Timeout,
		true,
		msg.RepeatedFrequency, msg.RepeatedTotal, types.Pause, msg.ResponseThreshold, types.ModuleName)
	if err != nil {
		return err
	}

	k.setFeed(ctx, types.Feed{
		FeedName:         msg.FeedName,
		AggregateFunc:    msg.AggregateFunc,
		ValueJsonPath:    msg.ValueJsonPath,
		LatestHistory:    msg.LatestHistory,
		RequestContextID: requestContextID,
		Creator:          msg.Creator,
	})
	return nil
}

//StartFeed start a stopped feed
func (k Keeper) StartFeed(ctx sdk.Context, msg types.MsgStartFeed) sdk.Error {
	feed, found := k.GetFeed(ctx, msg.FeedName)
	if !found {
		return types.ErrUnknownFeedName(types.DefaultCodespace, msg.FeedName)
	}

	if !msg.Creator.Equals(feed.Creator) {
		return types.ErrUnauthorized(types.DefaultCodespace, msg.FeedName, msg.Creator)
	}

	reqCtx, existed := k.sk.GetRequestContext(ctx, feed.RequestContextID)
	if !existed {
		return types.ErrUnknownFeedName(types.DefaultCodespace, msg.FeedName)
	}

	//Can not start feed in "running" state
	if reqCtx.State == types.Running {
		return types.ErrInvalidFeedState(types.DefaultCodespace, msg.FeedName)
	}

	if err := k.sk.StartRequestContext(ctx, feed.RequestContextID); err != nil {
		return err
	}
	return nil
}

//PauseFeed pause a running feed
func (k Keeper) PauseFeed(ctx sdk.Context, msg types.MsgPauseFeed) sdk.Error {
	feed, found := k.GetFeed(ctx, msg.FeedName)
	if !found {
		return types.ErrUnknownFeedName(types.DefaultCodespace, msg.FeedName)
	}

	if !msg.Creator.Equals(feed.Creator) {
		return types.ErrUnauthorized(types.DefaultCodespace, msg.FeedName, msg.Creator)
	}

	reqCtx, existed := k.sk.GetRequestContext(ctx, feed.RequestContextID)
	if !existed {
		return types.ErrUnknownFeedName(types.DefaultCodespace, msg.FeedName)
	}

	//Can only pause feed in "running" state
	if reqCtx.State != types.Running {
		return types.ErrInvalidFeedState(types.DefaultCodespace, msg.FeedName)
	}

	if err := k.sk.PauseRequestContext(ctx, feed.RequestContextID); err != nil {
		return err
	}
	return nil
}

//EditFeed edit a feed
func (k Keeper) EditFeed(ctx sdk.Context, msg types.MsgEditFeed) sdk.Error {
	feed, found := k.GetFeed(ctx, msg.FeedName)
	if !found {
		return types.ErrUnknownFeedName(types.DefaultCodespace, msg.FeedName)
	}

	if !msg.Creator.Equals(feed.Creator) {
		return types.ErrUnauthorized(types.DefaultCodespace, msg.FeedName, msg.Creator)
	}

	if msg.LatestHistory > 1 {
		if msg.LatestHistory != feed.LatestHistory &&
			msg.LatestHistory < feed.LatestHistory {
			count := int(feed.LatestHistory - msg.LatestHistory)
			k.deleteOldestFeedResult(ctx, feed.FeedName, count)
		}
		feed.LatestHistory = msg.LatestHistory
	}

	if err := k.sk.UpdateRequestContext(ctx, feed.RequestContextID,
		msg.Providers,
		msg.ServiceFeeCap,
		msg.RepeatedFrequency,
		msg.RepeatedTotal); err != nil {
		return err
	}
	k.setFeed(ctx, feed)
	return nil
}

//HandlerResponse is responsible for processing the data returned from the service module,
//processed by the aggregate function, and then saved
func (k Keeper) HandlerResponse(ctx sdk.Context, requestContextID []byte, responseOutput []string) {
	if len(responseOutput) == 0 {
		return
	}

	feed, found := k.GetFeedByReqCtxID(ctx, requestContextID)
	if !found {
		return
	}

	reqCtx, existed := k.sk.GetRequestContext(ctx, requestContextID)
	if !existed {
		return
	}

	aggregate, err := types.GetAggregateFunc(feed.AggregateFunc)
	if err != nil {
		return
	}

	var data []types.ArgsType
	for _, jsonStr := range responseOutput {
		result := gjson.Get(jsonStr, feed.ValueJsonPath)
		data = append(data, result)
	}
	result := aggregate(data)
	k.setFeedResult(ctx, feed.FeedName, reqCtx.BatchCounter, feed.LatestHistory, result)
}