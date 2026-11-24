inherit "/std/thing";       /* raw-inherit-path when enabled */

int naked_count;

static void
create()
{
    /* BUG: does not chain ::create() */
    naked_count = 1;
    unknowable()->whatever_fn();            /* unknown target: never flagged */
}

no_spec_fn()
{
    /* missing-visibility when enabled; a dgdtools-ignore comment makes
       line-scoped suppression testable on the following definition */
    return 0;
}

/* dgdtools-ignore lint/missing-visibility */
suppressed_fn()
{
    return 0;
}
