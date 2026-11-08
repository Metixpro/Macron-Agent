package telegram

import (
	"strings"
	"testing"
)

func TestMarkRTL(t *testing.T) {
	const r, l = "‏", "‎"
	for _, c := range []struct{ in, want string }{
		{"VIP فعال است", r + "VIP فعال است"},
		{"<b>Free</b> رایگان است", r + "<b>Free</b> رایگان است"},
		{"💎 VIP تا ۳۰ آبان", r + "💎 VIP تا ۳۰ آبان"},
		{"💰 ۲۰ ⭐", r + "💰 ۲۰ ⭐"},
		{"سطح: VIP", "سطح: VIP"},
		{"لغو با /reminders", "لغو با " + l + "/reminders"},
		{"برای انصراف <code>/cancel</code> رو بفرست", "برای انصراف " + l + "<code>/cancel</code> رو بفرست"},
		{"/me — حساب من", r + l + "/me — حساب من"},
		{"بفرست برای @someone", "بفرست برای " + l + "@someone"},
		{"https://t.me/bot?start=ref_1 لینک", "https://t.me/bot?start=ref_1 لینک"},
		{"gpt-4o (default)", "gpt-4o (default)"},
		{"Send /me to Ali", "Send /me to Ali"},
		{"۱۰/۲۰ و/یا a/b", "۱۰/۲۰ و/یا a/b"},
		{"&lt;tag&gt; یعنی برچسب", r + "&lt;tag&gt; یعنی برچسب"},
		{"&laquo; &amp; یعنی و", "&laquo; &amp; یعنی و"},
		{r + "VIP", r + "VIP"},
		{"", ""},
		{"   ", "   "},
	} {
		if got := MarkRTL(c.in); got != c.want {
			t.Errorf("MarkRTL(%q) = %q, want %q", c.in, got, c.want)
		} else if UnmarkRTL(got) != c.in && !strings.HasPrefix(c.in, r) {
			t.Errorf("UnmarkRTL(%q) = %q", got, UnmarkRTL(got))
		} else if again := MarkRTL(got); again != got {
			t.Errorf("MarkRTL twice on %q = %q", c.in, again) // a message edited from its own text
		}
	}
	in := "📊 <b>آمار</b>\n<pre>VIP 12\nFree 30\n</pre>\nVIP: ۱۲ نفر"
	want := "📊 <b>آمار</b>\n<pre>VIP 12\nFree 30\n</pre>\n" + r + "VIP: ۱۲ نفر"
	if got := MarkRTL(in); got != want {
		t.Errorf("pre:\n%q\nwant\n%q", got, want)
	}
}
