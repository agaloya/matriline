// The path pickers of the command forms (picker.go): a click on a name puts it in the
// field and marks it; the arrows open and close the folders. Typing in the field marks
// the name it matches and opens the folders around it.
document.querySelectorAll('.picker').forEach(function (box) {
	var field = document.getElementsByName(box.dataset.for)[0];
	if (!field) return;
	function mark(scroll) {
		var v = field.value.trim().replace(/\/+$/, '');
		box.querySelectorAll('.pk').forEach(function (b) {
			var on = b.dataset.p === v;
			b.classList.toggle('sel', on);
			b.setAttribute('aria-pressed', on ? 'true' : 'false');
			if (!on) return;
			for (var d = b.parentElement; d && d !== box; d = d.parentElement) {
				if (d.tagName === 'DETAILS' && d.firstElementChild !== b.parentElement) d.open = true;
			}
			if (scroll) box.scrollTop = b.offsetTop - box.clientHeight / 3; // .picker is position:relative
		});
	}
	box.addEventListener('click', function (e) {
		var b = e.target.closest('.pk');
		if (!b) return;
		e.preventDefault(); // a folder's name chooses it; its arrow opens it
		field.value = b.dataset.p;
		mark(false);
	});
	field.addEventListener('input', function () { mark(true); });
	mark(true);
});
