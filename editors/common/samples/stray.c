/*
 * Tokens the lexer has that only a macro body can hold: outside a
 * directive they are tokens still, and no valid file has them.
 */
static void
stray()
{
    a . b;
    a # b;
    a ## b;
}
