package query

import "github.com/permaily-org/permaily/internal/contracts"

// english is the sentence's table in English (ADR 0027, "Templates
// (English)"). A template's fixed words are written around its value: before
// it, and after it.
var english = language{
	tag:          "en",
	all:          "All mail.",
	never:        "No mail.",
	one:          "Mail ",
	several:      "Mail that meets all of these:",
	rule:         "Any of these:",
	item:         "- ",
	or:           ", or ",
	end:          ".",
	model:        " (a model decides)",
	utc:          " Times are in UTC.",
	months:       [12]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
	thousands:    ",",
	megabytes:    " MB",
	kilobytes:    " KB",
	byte:         " byte",
	bytes:        " bytes",
	yourMailbox:  "your mailbox ",
	disconnected: [2]string{"a mailbox you disconnected (", ")"},
	templates: map[templateKey]template{
		{contracts.VarFromAddress, contracts.OpIs, false, false}: {"sent from ", ""},
		{contracts.VarFromAddress, contracts.OpIs, true, false}:  {"not sent from ", ""},
		{contracts.VarFromDomain, contracts.OpIs, false, false}:  {"sent from an address at ", " or a subdomain of it"},
		{contracts.VarFromDomain, contracts.OpIs, true, false}:   {"not sent from any address at ", " or a subdomain of it"},

		{contracts.VarRecipientAddress, contracts.OpIs, false, false}: {"addressed to ", ""},
		{contracts.VarRecipientAddress, contracts.OpIs, true, false}:  {"not addressed to ", ""},
		{contracts.VarRecipientDomain, contracts.OpIs, false, false}:  {"addressed to an address at ", " or a subdomain of it"},
		{contracts.VarRecipientDomain, contracts.OpIs, true, false}:   {"not addressed to any address at ", " or a subdomain of it"},

		{contracts.VarSignedBy, contracts.OpIs, false, false}: {"signed by ", " or a subdomain of it"},
		{contracts.VarSignedBy, contracts.OpIs, true, false}:  {"not signed by ", " or any subdomain of it"},

		{contracts.VarSubject, contracts.OpContains, false, false}: {"whose subject contains the text ", ""},
		{contracts.VarSubject, contracts.OpContains, true, false}:  {"whose subject doesn't contain the text ", ""},
		{contracts.VarBody, contracts.OpContains, false, false}:    {"whose body contains the text ", ""},
		{contracts.VarBody, contracts.OpContains, true, false}:     {"whose body doesn't contain the text ", ""},

		{contracts.VarAttachments, contracts.OpExists, false, false}:      {"with attachments"},
		{contracts.VarAttachments, contracts.OpExists, true, false}:       {"without attachments"},
		{contracts.VarAttachmentName, contracts.OpContains, false, false}: {"with an attachment whose name contains the text ", ""},
		{contracts.VarAttachmentName, contracts.OpContains, true, false}:  {"with no attachment whose name contains the text ", ""},
		{contracts.VarAttachmentType, contracts.OpIs, false, false}:       {"with an attachment of type ", ""},
		{contracts.VarAttachmentType, contracts.OpIs, true, false}:        {"with no attachment of type ", ""},
		{contracts.VarAttachmentType, contracts.OpIs, false, true}:        {"with an attachment of any ", " type"},
		{contracts.VarAttachmentType, contracts.OpIs, true, true}:         {"with no attachment of any ", " type"},
		{contracts.VarList, contracts.OpExists, false, false}:             {"sent to a mailing list"},
		{contracts.VarList, contracts.OpExists, true, false}:              {"not sent to a mailing list"},
		{contracts.VarList, contracts.OpIs, false, false}:                 {"sent to the mailing list ", ""},
		{contracts.VarList, contracts.OpIs, true, false}:                  {"not sent to the mailing list ", ""},
		{contracts.VarReceived, contracts.OpSince, false, false}:          {"received on or after ", ""},
		{contracts.VarReceived, contracts.OpBefore, false, false}:         {"received before ", ""},
		{contracts.VarSize, contracts.OpAtLeast, false, false}:            {"of ", " or more"},
		{contracts.VarSize, contracts.OpBelow, false, false}:              {"smaller than ", ""},
		{contracts.VarMailbox, contracts.OpIs, false, false}:              {"that arrived at ", ""},
		{contracts.VarMailbox, contracts.OpIs, true, false}:               {"that didn't arrive at ", ""},
		{contracts.VarSubject, contracts.OpAbout, false, false}:           {"whose subject is about ", ""},
		{contracts.VarSubject, contracts.OpAbout, true, false}:            {"whose subject is not about ", ""},
		{contracts.VarMessage, contracts.OpAbout, false, false}:           {"about ", ""},
		{contracts.VarMessage, contracts.OpAbout, true, false}:            {"not about ", ""},
		{contracts.VarMessage, contracts.OpMentions, false, false}:        {"that mentions ", ""},
		{contracts.VarMessage, contracts.OpMentions, true, false}:         {"that doesn't mention ", ""},
	},
	// A condition no template above describes is not in a canonical
	// formula; were one rendered, it says what it is, its variable, its
	// operator and its value quoted, rather than nothing.
	unknown: [2]template{
		{"meeting the condition ", " ", " ", ""},
		{"not meeting the condition ", " ", " ", ""},
	},
}
