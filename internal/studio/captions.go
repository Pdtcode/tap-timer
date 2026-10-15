package studio

// Caption templates use {title} and {link}. The New post page fills {link}
// in on the server (BASE_URL/get?src=<platform src>) and {title} as you type,
// and you can still edit each caption before posting.
//
// None of the three platforms make links in a Short/Reel/TikTok caption
// clickable, so the defaults point at the bio link. Set each bio to that
// platform's /get link (the Connections page lists them) and installs from it
// are attributed per platform.
var defaultTemplates = map[string]string{
	"youtube":   "{title}\n\nFree to try: Tap Timer on the App Store, or play in your browser at {link}\n\n#shorts #taptimer #reactiontime #challenge",
	"instagram": "{title}\n\nFree to try. Link in bio.\n\n#taptimer #reactiontime #challenge #iphonegames #reels",
	"tiktok":    "{title} Free to try, link in bio. #taptimer #reactiontime #challenge #fyp",
}
