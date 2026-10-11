/* One page: is this TouchPad paired with a phone, and if not, pair it.
	It is shown two ways: from the launcher, and inside the Accounts app when
	"Google Messages" is picked under "Add an account". In the second case it
	hands the pairing back to Accounts when it is done. */
enyo.kind({
	name: "GM.App",
	kind: enyo.VFlexBox,
	className: "gm-app",
	components: [
		{kind: "ApplicationEvents", onWindowActivated: "activated", onWindowDeactivated: "deactivated"},
		{name: "result", kind: "CrossAppResult"},
		{kind: "Scroller", flex: 1, accelerated: false, components: [
			{className: "gm-page", components: [
				{className: "gm-title", content: "Google Messages"},
				{name: "status", className: "gm-status"},
				{name: "error", className: "gm-error", showing: false},

				// Not paired: the ways to pair.
				{name: "choose", showing: false, components: [
					{className: "gm-help", content: "Pair this TouchPad with the Google Messages app on your Android phone. " +
						"Your conversations then appear in Messaging, and replies go out through the phone."},
					{kind: "RowGroup", caption: "On this TouchPad (Atlas)", components: [
						{className: "gm-help gm-in-group", content: "In the Atlas browser, sign in to Google and open " +
							"messages.google.com once. Then tap this. Nothing leaves the tablet."},
						{kind: "Button", caption: "Pair from Atlas", onclick: "atlasClick"}
					]},
					{kind: "RowGroup", caption: "With a QR code", components: [
						{className: "gm-help gm-in-group", content: "On the phone: Google Messages, your profile picture, " +
							"Device pairing, QR code scanner. Not every country offers this."},
						{kind: "Button", caption: "Show a QR Code", onclick: "qrClick"}
					]},
					{kind: "RowGroup", caption: "With your phone's browser", components: [
						{className: "gm-help gm-in-group", content: "For phones without QR pairing, and no computer needed. " +
							"The phone needs the Firefox browser with its Cookie-Editor add-on, and to be on the same Wi-Fi as this TouchPad."},
						{kind: "Button", caption: "Pair from the Phone", onclick: "phoneClick"}
					]},
					{kind: "RowGroup", caption: "With a file from a computer", components: [
						{name: "fileHelp", className: "gm-help gm-in-group", allowHtml: true},
						{kind: "Button", caption: "Use the File", onclick: "fileClick"},
						{name: "again", kind: "Button", caption: "Try Again with the Last Sign-in", showing: false, onclick: "againClick"}
					]}
				]},

				// Waiting for the phone's browser.
				{name: "phoneBox", showing: false, components: [
					{className: "gm-help", content: "On the phone, in Firefox, open this address and follow the steps there:"},
					{name: "phoneUrl", className: "gm-big"},
					{className: "gm-help", content: "It will ask for this code:"},
					{name: "phoneCode", className: "gm-big gm-code"},
					{className: "gm-help", content: "The address works for ten minutes, and only on this Wi-Fi network."},
					{kind: "Button", caption: "Cancel", className: "gm-narrow", onclick: "cancelClick"}
				]},

				// Pairing: a code to scan, or a picture to tap.
				{name: "qrBox", showing: false, className: "gm-center", components: [
					{className: "gm-help", content: "Scan this with the phone: Google Messages, your profile picture, " +
						"Device pairing, QR code scanner. The code renews itself every half minute."},
					{name: "qr", kind: "Image", className: "gm-qr"},
					{kind: "Button", caption: "Cancel", className: "gm-narrow", onclick: "cancelClick"}
				]},
				{name: "emojiBox", showing: false, className: "gm-center", components: [
					{className: "gm-help", content: "Your phone is showing three pictures. Tap this one:"},
					{name: "emoji", kind: "Image", className: "gm-emoji"},
					{kind: "Button", caption: "Cancel", className: "gm-narrow", onclick: "cancelClick"}
				]},

				// Paired.
				{name: "paired", showing: false, components: [
					{name: "pairedHelp", className: "gm-help"},
					{name: "unpair", kind: "Button", caption: "Unpair", className: "enyo-button-negative gm-narrow", onclick: "unpairClick"}
				]}
			]}
		]},
		{name: "confirm", kind: "ModalDialog", lazy: false, caption: "Unpair", components: [
			{className: "gm-dialog-text", content: "Unpair from the phone? Its conversations are removed from Messaging on this TouchPad. " +
				"Nothing is deleted on the phone."},
			{kind: "HFlexBox", components: [
				{kind: "Button", caption: "Cancel", flex: 1, onclick: "confirmCancel"},
				{kind: "Button", caption: "Unpair", flex: 1, className: "enyo-button-negative", onclick: "confirmUnpair"}
			]}
		]}
	],

	create: function() {
		this.inherited(arguments);
		// Inside the Accounts app, the page is given the account template it is for.
		var params = enyo.windowParams || {};
		this.hosted = !!(params.template || params.account);
		this.mode = params.mode || "";
		this.sent = false;
		this.state = "";
		this.$.fileHelp.setContent("For phones without QR pairing. On a computer, in a private browser window, sign in to " +
			"Google and open messages.google.com/web/config. Copy that request from the browser's developer tools " +
			"(Network, right-click, Copy as cURL) into a text file named <b>" + GM.COOKIE_FILE + "</b>, and put the file " +
			"on this TouchPad's USB drive. It is a full sign-in to your Google account: the file is deleted once read, " +
			"and the sign-in is kept on this TouchPad.");
	},

	rendered: function() {
		this.inherited(arguments);
		if (!this.begun) {
			this.begun = true;
			this.watch(true);
		}
	},

	activated: function() {
		this.watch(true);
	},

	deactivated: function() {
		// Hosted pages get no window events of their own; only a card stops watching.
		if (!this.hosted) {
			this.watch(false);
		}
	},

	// Ask the relay how things stand, now and every few seconds.
	watch: function(on) {
		var self = this;
		clearInterval(this.timer);
		this.timer = null;
		if (on) {
			this.ask();
			this.timer = setInterval(function() { self.ask(); }, 2500);
		}
	},

	ask: function() {
		var self = this;
		GM.Relay.get("/status", function(error, status) {
			if (error) {
				self.showState("offline", {});
			} else {
				self.showState(status.state, status);
			}
		}, 8000);
	},

	showError: function(text) {
		this.$.error.setContent(text);
		this.$.error.setShowing(!!text);
	},

	showState: function(state, status) {
		var words = {
			offline: "The relay is not running on this TouchPad. Restart the TouchPad; if this stays, reinstall.",
			unpaired: "Not paired with a phone.",
			pairing: "Pairing…",
			connecting: "Connecting to the phone…",
			ready: "Connected to the phone" + (status.me ? " (" + status.me + ")." : "."),
			phoneAway: "Paired, but the phone is not answering. It needs to be on and online.",
			error: "Paired, but there is a problem."
		};
		var isPaired = !!status.paired;
		this.$.status.setContent(words[state] || state);
		// A problem is told by the relay in its own words; one from a tap here stays until the state moves on.
		if (status.error) {
			this.showError(status.error);
		} else if (state !== this.state) {
			this.showError("");
		}
		var handoff = state === "unpaired" ? status.handoff : null;
		this.$.choose.setShowing(state === "unpaired" && !handoff);
		this.$.phoneBox.setShowing(!!handoff);
		if (handoff) {
			this.$.phoneUrl.setContent(String(handoff.url).replace(/^http:\/\//, ""));
			// In two groups of three, as codes are read out.
			this.$.phoneCode.setContent(String(handoff.code).replace(/^(\d{3})(\d{3})$/, "$1 $2"));
		}
		this.$.again.setShowing(!!status.cookies);
		this.$.qrBox.setShowing(state === "pairing" && !!status.qr);
		this.$.emojiBox.setShowing(state === "pairing" && !!status.emoji);
		if (state === "pairing" && status.qr) {
			// A new address each time, so the renewed code is fetched.
			this.$.qr.setSrc(GM.RELAY + "/qr.png?" + new Date().getTime());
		}
		if (state === "pairing" && status.emoji && this.shownEmoji !== status.emoji) {
			this.shownEmoji = status.emoji;
			this.$.emoji.setSrc(GM.RELAY + "/emoji.png?" + new Date().getTime());
		}
		if (state !== "pairing") {
			this.shownEmoji = "";
		}
		this.$.paired.setShowing(isPaired && state !== "pairing");
		this.$.unpair.setShowing(!this.hosted);
		this.$.pairedHelp.setContent(this.hosted ?
			(this.mode === "modify" ? "This TouchPad is paired. To unpair, remove the account." : "Paired. One moment…") :
			"Your conversations are in the Messaging app. Removing the Google Messages account in Accounts also unpairs.");
		this.state = state;
		// Inside Accounts, a finished pairing is handed back so the account can be made.
		// The phone's number is the account's name, so wait for it.
		if (this.hosted && this.mode !== "modify" && !this.sent && isPaired && state === "ready" && status.me) {
			this.sent = true;
			this.$.result.sendResult({
				returnValue: true,
				username: status.me,
				alias: "Google Messages",
				credentials: {common: {paired: "true"}}
			});
		}
	},

	// ---- pairing ----

	began: function(error) {
		if (error) {
			this.showError(error === "offline" ? "The relay is not running." : error);
		}
		this.ask();
	},

	qrClick: function() {
		var self = this;
		this.showError("");
		this.$.status.setContent("Asking for a code…");
		GM.Relay.post("/pair" + (this.hosted ? "?external=1" : ""), {}, function(error) { self.began(error); }, 40000);
	},

	phoneClick: function() {
		var self = this;
		this.showError("");
		GM.Relay.post("/pair/phone" + (this.hosted ? "?external=1" : ""), {}, function(error) { self.began(error); });
	},

	atlasClick: function() {
		var self = this;
		this.showError("");
		this.$.status.setContent("Reading the Atlas sign-in…");
		GM.Relay.post("/pair/atlas" + (this.hosted ? "?external=1" : ""), {}, function(error) { self.began(error); });
	},

	fileClick: function() {
		this.googlePair({file: "/media/internal/" + GM.COOKIE_FILE, external: this.hosted});
	},

	againClick: function() {
		this.googlePair({external: this.hosted});
	},

	googlePair: function(body) {
		var self = this;
		this.showError("");
		this.$.status.setContent("Signing in and reaching the phone…");
		// The phone has to answer before there is a picture to show.
		GM.Relay.post("/pair/google", body, function(error) { self.began(error); }, 100000);
	},

	cancelClick: function() {
		var self = this;
		GM.Relay.post("/pair/cancel", {}, function() { self.ask(); });
	},

	// ---- unpairing ----

	unpairClick: function() {
		this.$.confirm.openAtCenter();
	},

	confirmCancel: function() {
		this.$.confirm.close();
	},

	confirmUnpair: function() {
		var self = this;
		this.$.confirm.close();
		this.$.status.setContent("Unpairing…");
		GM.Relay.post("/unpair", {}, function(error) { self.began(error); }, 40000);
	}
});
