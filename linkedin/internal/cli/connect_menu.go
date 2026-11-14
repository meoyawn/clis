package cli

import (
	"strings"

	"golang.org/x/net/html"
)

// A closed More menu is only in the hydration data. Bind its selected variant
// to the exact member's current model state, never to a dormant Connect action.
func connectionMenuAvailability(doc *html.Node, target person, ref profileRef) connectionAvailability {
	data := readDocumentData(doc)
	stateKey := "state:invitation:" + target.MemberURN
	var current string
	invalid := false
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			states, _ := m["modelStates"].([]any)
			for _, state := range states {
				switch jsonStringAt(state, "key", "key", "value", "id") {
				case stateKey:
					value := jsonStringAt(state, "value", "stringValue")
					if jsonStringAt(state, "value", "$case") != "stringValue" || value == "" || (current != "" && current != value) {
						invalid = true
					}
					current = value
				case "connect-button-disabled-" + ref.Slug:
					s, _ := state.(map[string]any)
					v, _ := s["value"].(map[string]any)
					if disabled, ok := v["booleanValue"].(bool); !ok || disabled {
						invalid = true
					}
				}
			}
		})
	}
	if invalid || (current != "Connect" && current != "Pending") {
		return connectionAvailability{}
	}
	label, suffix := "Invite "+target.Name+" to connect", "_connect"
	if current == "Pending" {
		label, suffix = "Pending, click to withdraw invitation sent to "+target.Name, "_pending"
	}
	found := false
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			if jsonStringAt(m, "viewTrackingSpecs", "viewName") != "profile-overflow-button" {
				return
			}
			data.walk(m["children"], func(action map[string]any) {
				if action["$type"] != "proto.sdui.actions.core.ShowMenu" {
					return
				}
				value, _ := action["value"].(map[string]any)
				data.walk(value["component"], func(selector map[string]any) {
					if jsonStringAt(selector, "selectedStateKey", "key", "value", "id") != stateKey {
						return
					}
					variants, _ := data.resolve(selector["states"]).([]any)
					var selected any
					matches := 0
					for _, variant := range variants {
						pair, ok := variant.([]any)
						if ok && len(pair) == 2 && pair[0] == current {
							selected, matches = pair[1], matches+1
						}
					}
					if matches != 1 {
						return
					}
					data.walk(selected, func(control map[string]any) {
						if control["role"] != "menuitem" || control["componentkey"] != "ConnectButton"+stateKey+suffix {
							return
						}
						data.walk(control["triggers"], func(trigger map[string]any) {
							if trigger["actionLabel"] != label {
								return
							}
							if current == "Pending" {
								found = true
								return
							}
							data.walk(trigger["actions"], func(navigation map[string]any) {
								if navigation["$type"] == "proto.sdui.actions.core.Navigate" && validConnectNavigation(navigation, target, ref) {
									found = true
								}
							})
						})
					})
				})
			})
		})
	}
	if !found {
		return connectionAvailability{}
	}
	return connectionAvailability{CanConnect: current == "Connect", Pending: current == "Pending", Evidence: "More menu: " + label}
}

func validConnectNavigation(action map[string]any, target person, ref profileRef) bool {
	if validConnectURL(jsonStringAt(action, "value", "content", "url", "urlValue", "url"), ref) {
		return true
	}
	// Native invitation-screen navigation was observed on 2026-11-13.
	// Its member, profile, destination and state bindings must all match.
	if jsonStringAt(action, "value", "content", "$case") != "screen" {
		return false
	}
	content, _ := action["value"].(map[string]any)
	content, _ = content["content"].(map[string]any)
	screen, _ := content["screen"].(map[string]any)
	if jsonStringAt(screen, "screenId") != "com.linkedin.sdui.flagshipnav.mynetwork.invitations.CustomInvite" {
		return false
	}
	arguments, _ := screen["requestedArguments"].(map[string]any)
	payload, _ := arguments["payload"].(map[string]any)
	return "urn:li:member:"+jsonStringAt(payload, "inviteeUrn", "memberId") == target.MemberURN &&
		jsonStringAt(payload, "nonIterableProfileId", "profileId") == target.ProfileURN &&
		strings.TrimSuffix(jsonStringAt(payload, "profileCanonicalUrl"), "/") == strings.TrimSuffix(target.URL, "/") &&
		jsonStringAt(payload, "connectionState", "value", "key") == "state:invitation:"+target.MemberURN &&
		jsonStringAt(payload, "isDisabled", "value", "key") == "connect-button-disabled-"+ref.Slug
}

func nativeConnectNavigationAvailable(doc *html.Node, target person, ref profileRef) bool {
	data := readDocumentData(doc)
	key := "ConnectButtonstate:invitation:" + target.MemberURN + "_connect"
	found := false
	for _, row := range data {
		data.walk(row, func(control map[string]any) {
			if control["componentKey"] != key && control["componentkey"] != key {
				return
			}
			data.walk(control, func(action map[string]any) {
				if action["$type"] == "proto.sdui.actions.core.Navigate" &&
					jsonStringAt(action, "value", "content", "$case") == "screen" && validConnectNavigation(action, target, ref) {
					found = true
				}
			})
		})
	}
	return found
}
