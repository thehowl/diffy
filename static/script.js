(function () {
	function updateTheme(newTheme) {
		document.documentElement.setAttribute("data-theme", newTheme);
		window.localStorage.setItem("data-theme", newTheme);
	}

	function updateSelectors() {
		document
			.querySelectorAll(".theme-selector [data-theme]")
			.forEach(function (sel) {
				if (
					sel.getAttribute("data-theme") ==
					document.documentElement.getAttribute("data-theme")
				) {
					sel.removeAttribute("href");
				} else {
					sel.setAttribute("href", "#");
				}
			});
	}

	updateSelectors();
	document
		.querySelectorAll(".theme-selector [data-theme]")
		.forEach(function (el) {
			el.addEventListener("click", function (e) {
				e.preventDefault();
				updateTheme(el.getAttribute("data-theme"));
				updateSelectors();
			});
		});
})();
