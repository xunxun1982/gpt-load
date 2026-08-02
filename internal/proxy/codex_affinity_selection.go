package proxy

import (
	"fmt"
	"net/url"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
)

type codexExecutionSelection struct {
	binding codexAffinityBinding
	apiKey  *models.APIKey
}

func (ps *ProxyServer) selectFreshCodexExecution(handler channel.ChannelProxy, group *models.Group, requestURL *url.URL, routeName string) (*codexExecutionSelection, error) {
	apiKey, err := ps.keyProvider.SelectKey(group.ID)
	if err != nil {
		return nil, err
	}
	upstream, err := handler.SelectUpstreamWithClients(requestURL, routeName)
	if err != nil {
		return nil, err
	}
	if upstream == nil || upstream.Identity == "" || upstream.URL == "" {
		return nil, fmt.Errorf("selected upstream has no stable identity")
	}
	return &codexExecutionSelection{
		binding: codexAffinityBinding{
			executionGroupID: group.ID,
			keyID:            apiKey.ID,
			upstreamIdentity: upstream.Identity,
		},
		apiKey: apiKey,
	}, nil
}

func (ps *ProxyServer) resolveCodexExecution(handler channel.ChannelProxy, group *models.Group, requestURL *url.URL, routeName string, binding codexAffinityBinding) (*codexExecutionSelection, error) {
	if !binding.valid() || binding.executionGroupID != group.ID {
		return nil, fmt.Errorf("Codex affinity binding does not match execution group")
	}
	apiKey, err := ps.keyProvider.GetActiveKeyByID(group.ID, binding.keyID)
	if err != nil {
		return nil, err
	}
	upstream, err := handler.ResolveUpstreamByIdentity(binding.upstreamIdentity, requestURL, routeName)
	if err != nil {
		return nil, err
	}
	if upstream == nil || upstream.URL == "" {
		return nil, fmt.Errorf("resolved upstream is empty")
	}
	return &codexExecutionSelection{binding: binding, apiKey: apiKey}, nil
}

func (ps *ProxyServer) prepareStandardCodexAffinity(c *gin.Context, handler channel.ChannelProxy, group *models.Group, body []byte, retryCtx *retryContext) ([]byte, error) {
	retryCtx.codexAffinityEnabled = codexAffinityEnabled(c, group)
	if !retryCtx.codexAffinityEnabled {
		return body, nil
	}
	retryCtx.codexAffinityCacheKey, retryCtx.codexStateDomainKey = codexAffinityRequestState(c, group, body)
	retryCtx.codexStateResetRequired = ps.codexAffinityCache.requiresStateReset(
		retryCtx.codexAffinityCacheKey, retryCtx.codexStateDomainKey, time.Now(),
	)
	if retryCtx.codexAffinityCacheKey != "" {
		if binding, ok := ps.codexAffinityCache.getBinding(retryCtx.codexAffinityCacheKey, time.Now()); ok {
			selection, err := ps.resolveCodexExecution(handler, group, c.Request.URL, group.Name, binding)
			if err == nil {
				retryCtx.codexSelection = selection
				retryCtx.codexAffinityBinding = binding
				retryCtx.codexAffinityUsingCached = true
				if retryCtx.codexStateResetRequired {
					return sanitizeCodexIdentityChange(c, body, group, false)
				}
				return body, nil
			}
			retryCtx.codexAffinityBinding = binding
			retryCtx.codexAffinityUsingCached = true
			ps.degradeCodexAffinity(retryCtx)
		}
	}
	selection, err := ps.selectFreshCodexExecution(handler, group, c.Request.URL, group.Name)
	if err != nil {
		return body, err
	}
	retryCtx.codexSelection = selection
	retryCtx.codexIdentityChanged = true
	return sanitizeCodexIdentityChange(c, body, group, codexEncryptedReasoningAllowed(retryCtx))
}

func (ps *ProxyServer) standardCodexDispatchSelection(c *gin.Context, handler channel.ChannelProxy, group *models.Group, body []byte, retryCtx *retryContext) (*models.APIKey, *channel.UpstreamSelection, []byte, error) {
	if retryCtx.codexSelection == nil {
		selection, err := ps.selectFreshCodexExecution(handler, group, c.Request.URL, group.Name)
		if err != nil {
			return nil, nil, body, err
		}
		retryCtx.codexSelection = selection
		retryCtx.codexIdentityChanged = true
		body, err = sanitizeCodexIdentityChange(c, body, group, codexEncryptedReasoningAllowed(retryCtx))
		if err != nil {
			return nil, nil, body, err
		}
	}

	upstream, err := handler.ResolveUpstreamByIdentity(retryCtx.codexSelection.binding.upstreamIdentity, c.Request.URL, group.Name)
	if err != nil || upstream == nil || upstream.URL == "" {
		ps.degradeCodexAffinity(retryCtx)
		selection, selectErr := ps.selectFreshCodexExecution(handler, group, c.Request.URL, group.Name)
		if selectErr != nil {
			return nil, nil, body, selectErr
		}
		retryCtx.codexSelection = selection
		retryCtx.codexIdentityChanged = true
		body, selectErr = sanitizeCodexIdentityChange(c, body, group, codexEncryptedReasoningAllowed(retryCtx))
		if selectErr != nil {
			return nil, nil, body, selectErr
		}
		upstream, selectErr = handler.ResolveUpstreamByIdentity(selection.binding.upstreamIdentity, c.Request.URL, group.Name)
		if selectErr != nil {
			return nil, nil, body, selectErr
		}
	}
	return retryCtx.codexSelection.apiKey, upstream, body, nil
}

func (ps *ProxyServer) cancelCodexAffinityBinding(retryCtx *retryContext) {
	if retryCtx == nil || !retryCtx.codexAffinityUsingCached {
		return
	}
	ps.codexAffinityCache.deleteBindingIfMatches(retryCtx.codexAffinityCacheKey, retryCtx.codexAffinityBinding)
	retryCtx.codexAffinityUsingCached = false
	retryCtx.codexAffinityBinding = codexAffinityBinding{}
	retryCtx.codexSelection = nil
	retryCtx.codexIdentityChanged = true
}

func (ps *ProxyServer) degradeCodexAffinity(retryCtx *retryContext) {
	if retryCtx == nil || !retryCtx.codexAffinityEnabled {
		return
	}
	ps.cancelCodexAffinityBinding(retryCtx)
	ps.codexAffinityCache.markStateReset(retryCtx.codexAffinityCacheKey, retryCtx.codexStateDomainKey, time.Now())
	retryCtx.codexAffinityDegraded = true
	retryCtx.codexStateResetRequired = true
	retryCtx.codexSelection = nil
	retryCtx.codexIdentityChanged = true
}
