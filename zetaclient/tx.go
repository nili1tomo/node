package zetaclient

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/pkg/errors"

	"github.com/zeta-chain/go-tss/blame"
	"github.com/zeta-chain/zetacore/common"
	"github.com/zeta-chain/zetacore/x/crosschain/types"
	observerTypes "github.com/zeta-chain/zetacore/x/observer/types"
	"github.com/zeta-chain/zetacore/zetaclient/config"
)

const (
	PostGasPriceGasLimit            = 1_500_000
	AddTxHashToOutTxTrackerGasLimit = 200_000
	PostNonceGasLimit               = 200_000
	PostSendEVMGasLimit             = 4_000_000 // likely emit a lot of logs, so costly
	PostSendNonEVMGasLimit          = 1_000_000
	PostReceiveConfirmationGasLimit = 400_000
	PostBlameDataGasLimit           = 200_000
	DefaultGasLimit                 = 200_000
	PostProveOutboundTxGasLimit     = 400_000
	DefaultRetryCount               = 5
	ExtendedRetryCount              = 15
	DefaultRetryInterval            = 5
)

// GetInBoundVoteMessage returns a new MsgVoteOnObservedInboundTx
func GetInBoundVoteMessage(
	sender string,
	senderChain int64,
	txOrigin string,
	receiver string,
	receiverChain int64,
	amount math.Uint,
	message string,
	inTxHash string,
	inBlockHeight uint64,
	gasLimit uint64,
	coinType common.CoinType,
	asset string,
	signerAddress string,
	eventIndex uint,
) *types.MsgVoteOnObservedInboundTx {
	msg := types.NewMsgVoteOnObservedInboundTx(
		signerAddress,
		sender,
		senderChain,
		txOrigin,
		receiver,
		receiverChain,
		amount,
		message,
		inTxHash,
		inBlockHeight,
		gasLimit,
		coinType,
		asset,
		eventIndex,
	)
	return msg
}

func (b *ZetaCoreBridge) WrapMessageWithAuthz(msg sdk.Msg) (sdk.Msg, AuthZSigner, error) {
	msgURL := sdk.MsgTypeURL(msg)

	// verify message validity
	if err := msg.ValidateBasic(); err != nil {
		return nil, AuthZSigner{}, fmt.Errorf("%s invalid msg | %s", msgURL, err.Error())
	}

	authzSigner := GetSigner(msgURL)
	authzMessage := authz.NewMsgExec(authzSigner.GranteeAddress, []sdk.Msg{msg})
	return &authzMessage, authzSigner, nil
}

func (b *ZetaCoreBridge) PostGasPrice(chain common.Chain, gasPrice uint64, supply string, blockNum uint64) (string, error) {
	signerAddress := b.keys.GetOperatorAddress().String()
	msg := types.NewMsgGasPriceVoter(signerAddress, chain.ChainId, gasPrice, supply, blockNum)

	authzMsg, authzSigner, err := b.WrapMessageWithAuthz(msg)
	if err != nil {
		return "", err
	}

	for i := 0; i < DefaultRetryCount; i++ {
		zetaTxHash, err := b.Broadcast(PostGasPriceGasLimit, authzMsg, authzSigner)
		if err == nil {
			return zetaTxHash, nil
		}
		b.logger.Debug().Err(err).Msgf("PostGasPrice broadcast fail | Retry count : %d", i+1)
		time.Sleep(DefaultRetryInterval * time.Second)
	}

	return "", fmt.Errorf("post gasprice failed after %d retries", DefaultRetryInterval)
}

func (b *ZetaCoreBridge) AddTxHashToOutTxTracker(
	chainID int64,
	nonce uint64,
	txHash string,
	proof *common.Proof,
	blockHash string,
	txIndex int64,
) (string, error) {
	signerAddress := b.keys.GetOperatorAddress().String()
	msg := types.NewMsgAddToOutTxTracker(signerAddress, chainID, nonce, txHash, proof, blockHash, txIndex)

	authzMsg, authzSigner, err := b.WrapMessageWithAuthz(msg)
	if err != nil {
		return "", err
	}

	zetaTxHash, err := b.Broadcast(AddTxHashToOutTxTrackerGasLimit, authzMsg, authzSigner)
	if err != nil {
		return "", err
	}
	return zetaTxHash, nil
}

func (b *ZetaCoreBridge) PostSend(zetaGasLimit uint64, msg *types.MsgVoteOnObservedInboundTx) (string, string, error) {
	authzMsg, authzSigner, err := b.WrapMessageWithAuthz(msg)
	if err != nil {
		return "", "", err
	}

	// don't post send if has already voted before
	ballotIndex := msg.Digest()
	hasVoted, err := b.HasVoted(ballotIndex, msg.Creator)
	if err != nil {
		return "", ballotIndex, errors.Wrapf(err, "PostSend: unable to check if already voted for ballot %s voter %s", ballotIndex, msg.Creator)
	}
	if hasVoted {
		return "", ballotIndex, nil
	}

	for i := 0; i < DefaultRetryCount; i++ {
		zetaTxHash, err := b.Broadcast(zetaGasLimit, authzMsg, authzSigner)
		if err == nil {
			// monitor the result of the transaction
			go b.MonitorTxResult(zetaTxHash, true)

			return zetaTxHash, ballotIndex, nil
		}
		b.logger.Debug().Err(err).Msgf("PostSend broadcast fail | Retry count : %d", i+1)
		time.Sleep(DefaultRetryInterval * time.Second)
	}
	return "", ballotIndex, fmt.Errorf("post send failed after %d retries", DefaultRetryInterval)
}

func (b *ZetaCoreBridge) PostReceiveConfirmation(
	sendHash string,
	outTxHash string,
	outBlockHeight uint64,
	outTxGasUsed uint64,
	outTxEffectiveGasPrice *big.Int,
	outTxEffectiveGasLimit uint64,
	amount *big.Int,
	status common.ReceiveStatus,
	chain common.Chain,
	nonce uint64,
	coinType common.CoinType,
) (string, string, error) {
	signerAddress := b.keys.GetOperatorAddress().String()
	msg := types.NewMsgVoteOnObservedOutboundTx(
		signerAddress,
		sendHash,
		outTxHash,
		outBlockHeight,
		outTxGasUsed,
		math.NewIntFromBigInt(outTxEffectiveGasPrice),
		outTxEffectiveGasLimit,
		math.NewUintFromBigInt(amount),
		status,
		chain.ChainId,
		nonce,
		coinType,
	)

	authzMsg, authzSigner, err := b.WrapMessageWithAuthz(msg)
	if err != nil {
		return "", "", err
	}

	// don't post confirmation if has already voted before
	ballotIndex := msg.Digest()
	hasVoted, err := b.HasVoted(ballotIndex, msg.Creator)
	if err != nil {
		return "", ballotIndex, errors.Wrapf(err, "PostReceiveConfirmation: unable to check if already voted for ballot %s voter %s", ballotIndex, msg.Creator)
	}
	if hasVoted {
		return "", ballotIndex, nil
	}

	// FIXME: remove this gas limit stuff; in the special ante handler with no gas limit, add
	// NewMsgReceiveConfirmation to it.
	var gasLimit uint64 = PostReceiveConfirmationGasLimit
	if status == common.ReceiveStatus_Failed {
		gasLimit = PostSendEVMGasLimit
	}
	for i := 0; i < DefaultRetryCount; i++ {
		zetaTxHash, err := b.Broadcast(gasLimit, authzMsg, authzSigner)
		if err == nil {
			// monitor the result of the transaction
			go b.MonitorTxResult(zetaTxHash, true)

			return zetaTxHash, ballotIndex, nil
		}
		b.logger.Debug().Err(err).Msgf("PostReceive broadcast fail | Retry count : %d", i+1)
		time.Sleep(DefaultRetryInterval * time.Second)
	}
	return "", ballotIndex, fmt.Errorf("post receive failed after %d retries", DefaultRetryCount)
}

func (b *ZetaCoreBridge) SetTSS(tssPubkey string, keyGenZetaHeight int64, status common.ReceiveStatus) (string, error) {
	signerAddress := b.keys.GetOperatorAddress().String()
	msg := types.NewMsgCreateTSSVoter(signerAddress, tssPubkey, keyGenZetaHeight, status)

	authzMsg, authzSigner, err := b.WrapMessageWithAuthz(msg)
	if err != nil {
		return "", err
	}

	zetaTxHash := ""
	for i := 0; i <= DefaultRetryCount; i++ {
		zetaTxHash, err = b.Broadcast(DefaultGasLimit, authzMsg, authzSigner)
		if err == nil {
			return zetaTxHash, nil
		}
		b.logger.Debug().Err(err).Msgf("SetTSS broadcast fail | Retry count : %d", i+1)
		time.Sleep(DefaultRetryInterval * time.Second)
	}

	return "", fmt.Errorf("set tss failed | err %s", err.Error())
}

func (b *ZetaCoreBridge) ConfigUpdater(cfg *config.Config) {
	b.logger.Info().Msg("ConfigUpdater started")
	ticker := time.NewTicker(time.Duration(cfg.ConfigUpdateTicker) * time.Second)
	for {
		select {
		case <-ticker.C:
			b.logger.Debug().Msg("Running Updater")
			err := b.UpdateConfigFromCore(cfg, false)
			if err != nil {
				b.logger.Err(err).Msg("ConfigUpdater failed to update config")
			}
		case <-b.stop:
			b.logger.Info().Msg("ConfigUpdater stopped")
			return
		}
	}
}

func (b *ZetaCoreBridge) PostBlameData(blame *blame.Blame, chainID int64, index string) (string, error) {
	signerAddress := b.keys.GetOperatorAddress().String()
	zetaBlame := observerTypes.Blame{
		Index:         index,
		FailureReason: blame.FailReason,
		Nodes:         observerTypes.ConvertNodes(blame.BlameNodes),
	}
	msg := observerTypes.NewMsgAddBlameVoteMsg(signerAddress, chainID, zetaBlame)

	authzMsg, authzSigner, err := b.WrapMessageWithAuthz(msg)
	if err != nil {
		return "", err
	}

	var gasLimit uint64 = PostBlameDataGasLimit

	for i := 0; i < DefaultRetryCount; i++ {
		zetaTxHash, err := b.Broadcast(gasLimit, authzMsg, authzSigner)
		if err == nil {
			return zetaTxHash, nil
		}
		b.logger.Error().Err(err).Msgf("PostBlame broadcast fail | Retry count : %d", i+1)
		time.Sleep(DefaultRetryInterval * time.Second)
	}
	return "", fmt.Errorf("post blame data failed after %d retries", DefaultRetryCount)
}

func (b *ZetaCoreBridge) PostAddBlockHeader(chainID int64, blockHash []byte, height int64, header common.HeaderData) (string, error) {
	signerAddress := b.keys.GetOperatorAddress().String()

	msg := observerTypes.NewMsgAddBlockHeader(signerAddress, chainID, blockHash, height, header)

	authzMsg, authzSigner, err := b.WrapMessageWithAuthz(msg)
	if err != nil {
		return "", err
	}

	var gasLimit uint64 = DefaultGasLimit
	for i := 0; i < DefaultRetryCount; i++ {
		zetaTxHash, err := b.Broadcast(gasLimit, authzMsg, authzSigner)
		if err == nil {
			return zetaTxHash, nil
		}
		b.logger.Error().Err(err).Msgf("PostAddBlockHeader broadcast fail | Retry count : %d", i+1)
		time.Sleep(DefaultRetryInterval * time.Second)
	}
	return "", fmt.Errorf("post add block header failed after %d retries", DefaultRetryCount)
}

// MonitorTxResult monitors the result of a tx (used for inbound and outbound vote txs)
func (b *ZetaCoreBridge) MonitorTxResult(zetaTxHash string, isInbound bool) {
	var lastErr error
	ticker := 5 * time.Second
	retry := 10
	prefix := "MonitorOutboundTxResult"
	if isInbound {
		prefix = "MonitorInboundTxResult"
	}

	for i := 0; i < retry; i++ {
		time.Sleep(ticker)
		txResult, err := b.QueryTxResult(zetaTxHash)
		if err == nil {
			// the inbound vote tx shouldn't fail to execute
			if strings.Contains(txResult.RawLog, "failed to execute message") {
				b.logger.Error().Msgf(
					"%s: failed to execute vote, txHash: %s, txResult %s",
					prefix,
					zetaTxHash,
					txResult.String(),
				)
			}
			return
		}
		lastErr = err
	}

	b.logger.Error().Err(lastErr).Msgf(
		"%s: unable to query tx result for txHash %s, err %s",
		prefix,
		zetaTxHash,
		lastErr.Error(),
	)
	return
}
